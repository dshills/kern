package testrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// Config is one (engine, opt level) pair.
type Config struct {
	Engine string `json:"engine"` // "interp" or "native"
	Opt    string `json:"opt"`    // "debug" or "release"
}

// AllConfigs are the four configurations in the order of R6.
var AllConfigs = []Config{{"interp", "debug"}, {"interp", "release"}, {"native", "debug"}, {"native", "release"}}

// Skip names an engine left out and why (R6).
type Skip struct {
	Engine string `json:"engine"`
	Reason string `json:"reason"`
}

// TrapInfo is the compared part of a trap record (R8, R9).
type TrapInfo struct {
	Kind string    `json:"kind"`
	File string    `json:"file"`
	Span diag.Span `json:"span"`
}

// Run is one execution of one test in one configuration (R8, R11).
type Run struct {
	Engine          string    `json:"engine"`
	Opt             string    `json:"opt"`
	Outcome         string    `json:"outcome"` // return | trap | exit | crash | timeout
	Status          int       `json:"status"`
	Trap            *TrapInfo `json:"trap"`
	Stdout          string    `json:"stdout"`
	StdoutTruncated bool      `json:"stdout_truncated"`
	Millis          int64     `json:"millis"`
	out             output    // the captured output's head, length and digest, compared by Agree
}

// TestResult is one test's entry in the response (R11).
type TestResult struct {
	Name   string   `json:"name"`
	Loc    diag.Loc `json:"loc"`
	Expect Expect   `json:"expect"`
	Status string   `json:"status"` // pass | fail | mismatch | timeout | skipped
	Runs   []Run    `json:"runs"`
}

// Summary counts the tests by status.
type Summary struct {
	Pass     int `json:"pass"`
	Fail     int `json:"fail"`
	Mismatch int `json:"mismatch"`
	Timeout  int `json:"timeout"`
	Skipped  int `json:"skipped"`
}

// Result is the response's result object (R11).
type Result struct {
	Configurations []Config     `json:"configurations"`
	Skipped        []Skip       `json:"skipped"`
	Tests          []TestResult `json:"tests"`
	Summary        Summary      `json:"summary"`
}

// OK reports whether every test passed or was skipped.
func (r *Result) OK() bool { return r.Summary.Fail+r.Summary.Mismatch+r.Summary.Timeout == 0 }

// Options select what Execute runs.
type Options struct {
	Configs []Config
	Skipped []Skip
	Timeout time.Duration
	Filter  *regexp.Regexp // nil: every test
}

// MaxStdout is how many bytes of a run's output the response carries.
const MaxStdout = 65536

// InternalError is a failure of the compiler or the toolchain, not of a
// test (R11: exit 3).
type InternalError struct{ Err error }

func (e *InternalError) Error() string { return e.Err.Error() }

// Execute compiles the program once per selected opt level with its tests
// as roots and runs every selected test in every selected configuration
// (§2.5). Diagnostics are returned when the program or its tests do not
// check, and nothing runs; an *InternalError when a pass or the native
// build fails.
func Execute(srcs []*source.File, o Options) (*Result, []diag.Diagnostic, error) {
	res := &Result{Configurations: o.Configs, Skipped: o.Skipped, Tests: []TestResult{}}
	if res.Configurations == nil {
		res.Configurations = []Config{}
	}
	if res.Skipped == nil {
		res.Skipped = []Skip{}
	}
	type level struct {
		mod   *ir.Module
		tests []Test
	}
	levels := map[string]*level{}
	var tests []Test
	for _, c := range o.Configs {
		if levels[c.Opt] != nil {
			continue
		}
		prog := resolve.ResolveFor(srcs, c.Opt == "release")
		if len(prog.Diags) > 0 {
			return nil, prog.Diags, nil
		}
		if ds := check.Check(prog); len(ds) > 0 {
			return nil, ds, nil
		}
		found, ds := Find(prog, srcs)
		if len(ds) > 0 {
			return nil, ds, nil
		}
		var kept []Test
		for _, t := range found {
			if o.Filter == nil || o.Filter.MatchString(t.Name) {
				kept = append(kept, t)
			}
		}
		decls := make([]*ast.FuncDecl, len(kept))
		for i, t := range kept {
			decls[i] = t.Decl
		}
		mod, lds := lower.LowerWith(prog, lower.Options{Tests: decls})
		if len(lds) > 0 {
			return nil, nil, &InternalError{fmt.Errorf("lowering: %s", lds[0].Message)}
		}
		if err := opt.Run(mod, opt.OptionsFor(c.Opt)); err != nil {
			return nil, nil, &InternalError{fmt.Errorf("IR validation after %v", err)}
		}
		if len(mod.Tests) != len(kept) {
			return nil, nil, &InternalError{fmt.Errorf("lowering listed %d tests, want %d", len(mod.Tests), len(kept))}
		}
		for i, t := range kept {
			if mod.Tests[i] != t.Name {
				return nil, nil, &InternalError{fmt.Errorf("lowering named test %d %s, want %s", i, mod.Tests[i], t.Name)}
			}
		}
		levels[c.Opt] = &level{mod: mod, tests: kept}
		tests = kept
	}
	runs := make([][]Run, len(tests))
	exes := map[string]string{}
	var dir string
	defer func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, c := range o.Configs {
		lv := levels[c.Opt]
		if c.Engine == "native" && len(tests) > 0 && exes[c.Opt] == "" {
			if dir == "" {
				d, err := os.MkdirTemp("", "kern-test-")
				if err != nil {
					return nil, nil, &InternalError{err}
				}
				dir = d
			}
			exe := filepath.Join(dir, "tests-"+c.Opt)
			if err := native.BuildModule(lv.mod, srcs, exe, native.Options{Opt: c.Opt}); err != nil {
				return nil, nil, &InternalError{fmt.Errorf("native build (%s): %v", c.Opt, err)}
			}
			exes[c.Opt] = exe
		}
		for i, t := range lv.tests {
			if c.Opt == "release" && t.Expect.Outcome == "trap" && t.Expect.Kind == "identity" {
				continue // identity tags exist only in debug builds (R6)
			}
			var r Run
			var err error
			if c.Engine == "interp" {
				r, err = runInterp(lv.mod, t, c.Opt, o.Timeout)
			} else {
				r, err = runNative(exes[c.Opt], i, o.Timeout)
			}
			if err != nil {
				return nil, nil, &InternalError{fmt.Errorf("%s %s run of %s: %v", c.Engine, c.Opt, t.Name, err)}
			}
			r.Engine, r.Opt = c.Engine, c.Opt
			runs[i] = append(runs[i], r)
		}
	}
	for i, t := range tests {
		tr := TestResult{Name: t.Name, Loc: diag.Loc{File: t.File, Span: t.Span}, Expect: t.Expect, Runs: runs[i]}
		if tr.Runs == nil {
			tr.Runs = []Run{}
		}
		tr.Status = Status(t.Expect, tr.Runs)
		switch tr.Status {
		case "pass":
			res.Summary.Pass++
		case "fail":
			res.Summary.Fail++
		case "mismatch":
			res.Summary.Mismatch++
		case "timeout":
			res.Summary.Timeout++
		case "skipped":
			res.Summary.Skipped++
		}
		res.Tests = append(res.Tests, tr)
	}
	return res, nil, nil
}

// capture keeps what a run wrote within bounded memory (R8, R9): the
// first MaxStdout bytes, and the length and SHA-256 of everything, which
// is what agreement compares; with tail set, also the last tail bytes
// (standard error, whose last trap record is the one used). It is safe
// for concurrent use: an interpreter run's threads write to it, and after
// a timeout Close freezes it, discarding later writes.
type capture struct {
	mu     sync.Mutex
	head   []byte
	n      int64
	sum    hash.Hash
	tail   int
	last   []byte
	closed bool
}

func newCapture(tail int) *capture { return &capture{sum: sha256.New(), tail: tail} }

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return len(p), nil
	}
	if room := MaxStdout + 1 - len(c.head); room > 0 {
		c.head = append(c.head, p[:min(room, len(p))]...)
	}
	c.n += int64(len(p))
	c.sum.Write(p)
	switch {
	case c.tail == 0:
	case len(p) >= c.tail:
		c.last = append(c.last[:0], p[len(p)-c.tail:]...) // only the write's own tail
	default:
		c.last = append(c.last, p...)
		if len(c.last) > 2*c.tail {
			c.last = append([]byte(nil), c.last[len(c.last)-c.tail:]...)
		}
	}
	return len(p), nil
}

// Close freezes the capture: what a timed-out run reports is what it had
// written at the timeout.
func (c *capture) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// output is the captured head, total length and digest.
func (c *capture) output() output {
	c.mu.Lock()
	defer c.mu.Unlock()
	var d [sha256.Size]byte
	copy(d[:], c.sum.Sum(nil))
	return output{head: append([]byte(nil), c.head...), n: c.n, digest: d}
}

// tailBytes is the last bytes written (at most 2*tail).
func (c *capture) tailBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.last...)
}

// output is a run's standard output as kept: what the response shows and
// what agreement compares.
type output struct {
	head   []byte // at most MaxStdout+1 bytes
	n      int64
	digest [sha256.Size]byte
}

// outputOf is the output of a byte slice held whole (tests, Classify).
func outputOf(b []byte) output {
	c := newCapture(0)
	_, _ = c.Write(b)
	return c.output()
}

// stopGrace is how long a stopped interpreter run may take to reach its
// next basic block before the runner leaves it.
const stopGrace = time.Second

func runInterp(mod *ir.Module, t Test, optLevel string, timeout time.Duration) (Run, error) {
	out, errb := newCapture(0), newCapture(MaxStderrTail)
	it := interp.New(mod, t.File, out, errb)
	it.SetIdentityTags(optLevel != "release")
	type done struct {
		status int
		panic  any
	}
	ch := make(chan done, 1)
	start := time.Now()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- done{panic: r}
			}
		}()
		ch <- done{status: it.RunEntry(t.Name)}
	}()
	select {
	case d := <-ch:
		if d.panic != nil {
			return Run{}, fmt.Errorf("interpreter: %v", d.panic)
		}
		return classify(d.status, false, false, out.output(), errb.tailBytes(), time.Since(start)), nil
	case <-time.After(timeout):
		elapsed := time.Since(start)
		out.Close()
		errb.Close()
		it.Stop()
		select {
		case <-ch: // it reached a block boundary and ended
		case <-time.After(stopGrace): // blocked in a lock or a wait: left idle
		}
		return classify(-1, true, false, out.output(), nil, elapsed), nil
	}
}

// MaxStderrTail is how much of a run's standard error is kept: its end,
// where the trap record is.
const MaxStderrTail = 65536

func runNative(exe string, index int, timeout time.Duration) (Run, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, strconv.Itoa(index))
	out, errb := newCapture(0), newCapture(MaxStderrTail)
	cmd.Stdout, cmd.Stderr = out, errb
	cmd.WaitDelay = time.Second
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if ctx.Err() == context.DeadlineExceeded {
		return classify(-1, true, false, out.output(), nil, elapsed), nil
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return Run{}, err // the process did not start
	}
	status, signaled := 0, false
	if ps := cmd.ProcessState; ps != nil {
		status = ps.ExitCode()
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			status, signaled = 128+int(ws.Signal()), true
		}
	}
	return classify(status, false, signaled, out.output(), errb.tailBytes(), elapsed), nil
}

// Classify decides a run's outcome from what the run left (R8), its
// whole standard output and the end of its standard error.
func Classify(status int, timedOut, signaled bool, stdout, stderr []byte, elapsed time.Duration) Run {
	return classify(status, timedOut, signaled, outputOf(stdout), stderr, elapsed)
}

func classify(status int, timedOut, signaled bool, stdout output, stderr []byte, elapsed time.Duration) Run {
	r := Run{Status: status, out: stdout, Millis: elapsed.Milliseconds()}
	r.Stdout, r.StdoutTruncated = render(stdout)
	rec := lastRecord(stderr)
	switch {
	case timedOut:
		r.Outcome, r.Status = "timeout", -1
	case status == 134 && rec != nil:
		r.Outcome, r.Trap = "trap", rec
	case signaled || status == 134:
		r.Outcome = "crash"
	case status == 0:
		r.Outcome = "return"
	default:
		r.Outcome = "exit"
	}
	return r
}

// render is the response's form of a run's output: at most MaxStdout
// bytes, invalid UTF-8 replaced by U+FFFD.
func render(o output) (string, bool) {
	b := o.head
	truncated := o.n > MaxStdout
	if len(b) > MaxStdout {
		b = b[:MaxStdout]
	}
	return strings.ToValidUTF8(string(b), string(utf8.RuneError)), truncated
}

// lastRecord parses the last line of stderr that is a §6 trap record.
func lastRecord(stderr []byte) *TrapInfo {
	var found *TrapInfo
	for _, line := range bytes.Split(stderr, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte(`{"trap"`)) {
			continue
		}
		var rec struct {
			Trap struct {
				Kind string    `json:"kind"`
				File string    `json:"file"`
				Span diag.Span `json:"span"`
			} `json:"trap"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Trap.Kind != "" {
			found = &TrapInfo{Kind: rec.Trap.Kind, File: rec.Trap.File, Span: rec.Trap.Span}
		}
	}
	return found
}

// Agree reports whether two runs of one test agree (R9): the outcome, the
// whole output (by length and SHA-256), the status of an exit, and a trap's kind, file and span
// (neither the span nor the file of a stack trap, which the native fault
// handler cannot give, ADR-012). The record's thread is not compared.
func Agree(a, b Run) bool {
	if a.Outcome != b.Outcome || a.out.n != b.out.n || a.out.digest != b.out.digest {
		return false
	}
	switch a.Outcome {
	case "exit":
		return a.Status == b.Status
	case "trap":
		if a.Trap == nil || b.Trap == nil {
			return a.Trap == b.Trap
		}
		// a native stack record names the function the guard page was hit
		// in, which can differ from the interpreter's (ADR-012,
		// specs/native-trap-loc): neither its span nor its file is
		// compared (specs/runtime-trap-file R7)
		return a.Trap.Kind == b.Trap.Kind && (a.Trap.Kind == "stack" || a.Trap.File == b.Trap.File && a.Trap.Span == b.Trap.Span)
	}
	return true
}

// Status is a test's status from its runs (R10).
func Status(e Expect, runs []Run) string {
	if len(runs) == 0 {
		return "skipped"
	}
	for _, r := range runs {
		if r.Outcome == "timeout" {
			return "timeout"
		}
	}
	for _, r := range runs[1:] {
		if !Agree(runs[0], r) {
			return "mismatch"
		}
	}
	r := runs[0]
	switch {
	case e.Outcome == "return" && r.Outcome == "return":
		return "pass"
	case e.Outcome == "trap" && r.Outcome == "trap" && r.Trap != nil && r.Trap.Kind == e.Kind:
		return "pass"
	}
	return "fail"
}
