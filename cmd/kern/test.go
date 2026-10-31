package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/source"
	"github.com/dshills/kern/compiler/testrun"
)

// testCommand implements `kern test [--opt=O] [--engine=E] [--run=RE]
// [--timeout=S] [--jobs=N] [--human] [files...]` (specs/test-command/SPEC.md,
// specs/test-runs/SPEC.md): the
// test functions of the program's test files, run in the interpreter and
// natively in both opt levels, compared run against run. Exit 0 when every
// test passes (or is skipped), 1 when one does not, 2 on diagnostics or a
// flag error, 3 on an internal error.
func testCommand(args []string, stdout, stderr io.Writer) int {
	optLevel, engine := "", ""
	human := false
	timeout := 10 * time.Second
	jobs := runtime.NumCPU()
	var filter *regexp.Regexp
	var named []string
	for _, a := range args {
		k, v, hasValue := strings.Cut(a, "=")
		switch {
		case a == "--human":
			human = true
		case k == "--opt" && hasValue:
			if v != "debug" && v != "release" {
				return fail(stdout, "CLI_INVALID_FLAGS", "--opt must be debug or release")
			}
			optLevel = v
		case k == "--engine" && hasValue:
			if v != "interp" && v != "native" {
				return fail(stdout, "CLI_INVALID_FLAGS", "--engine must be interp or native")
			}
			engine = v
		case k == "--run" && hasValue:
			re, err := regexp.Compile(v)
			if err != nil {
				return fail(stdout, "CLI_INVALID_FLAGS", "--run: %v", err)
			}
			filter = re
		case k == "--timeout" && hasValue:
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 3600 {
				return fail(stdout, "CLI_INVALID_FLAGS", "--timeout must be a whole number of seconds from 1 to 3600")
			}
			timeout = time.Duration(n) * time.Second
		case k == "--jobs" && hasValue:
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 256 {
				return fail(stdout, "CLI_INVALID_FLAGS", "--jobs must be a whole number from 1 to 256")
			}
			jobs = n
		case k == "--env" && hasValue:
			if v != "hosted" {
				return fail(stdout, "CLI_INVALID_FLAGS", "kern test runs hosted only")
			}
		case a == "--test-hooks":
			return fail(stdout, "CLI_INVALID_FLAGS", "kern test does not take --test-hooks")
		case strings.HasPrefix(a, "-"):
			return fail(stdout, "CLI_INVALID_FLAGS", "unknown flag %s", a)
		default:
			named = append(named, a)
		}
	}
	cwd, _ := os.Getwd()
	pr := project.LoadTests(cwd, named)
	if len(pr.Diags) > 0 {
		return endWith(human, diag.SchemaVersion, pr.Diags, stdout)
	}
	if m := pr.Manifest; m != nil && m.Env != nil && *m.Env == "freestanding" {
		return fail(stdout, "CLI_INVALID_FLAGS", "kern test runs hosted only, and %s sets env freestanding", project.ManifestName)
	}
	var configs []testrun.Config
	var skipped []testrun.Skip
	_, clangErr := exec.LookPath("clang")
	if engine == "native" && clangErr != nil {
		return fail(stdout, "CLI_INVALID_FLAGS", "--engine=native needs clang, which is not on PATH")
	}
	for _, c := range testrun.AllConfigs {
		if optLevel != "" && c.Opt != optLevel || engine != "" && c.Engine != engine {
			continue
		}
		if c.Engine == "native" && clangErr != nil {
			continue
		}
		configs = append(configs, c)
	}
	if engine == "" && clangErr != nil {
		skipped = append(skipped, testrun.Skip{Engine: "native", Reason: "clang not found on PATH"})
	}
	res, ds, err := testHook(pr.Files, testrun.Options{Configs: configs, Skipped: skipped, Timeout: timeout, Filter: filter, Jobs: jobs})
	if err != nil {
		var ie *testrun.InternalError
		if !errors.As(err, &ie) {
			ie = &testrun.InternalError{Err: err}
		}
		req := Request{SchemaVersion: diag.SchemaVersion, Command: "test", Files: pr.Names()}
		return internal(req, diag.SchemaVersion, &InternalError{Err: ie.Err}, stdout, stderr)
	}
	if len(ds) > 0 {
		return endWith(human, diag.SchemaVersion, ds, stdout)
	}
	code := 0
	if !res.OK() {
		code = 1
	}
	if human {
		writeHumanTests(stdout, res, pr.Files)
		return code
	}
	if err := diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: res.OK(), Result: res, Diagnostics: []diag.Diagnostic{}}); err != nil {
		return 3
	}
	return code
}

// testHook runs the tests; CLI tests replace it to inject failures.
var testHook = testrun.Execute

// writeHumanTests writes one line per test and a summary (R11).
func writeHumanTests(w io.Writer, res *testrun.Result, srcs []*source.File) {
	label := map[string]string{"pass": "PASS", "fail": "FAIL", "mismatch": "MISMATCH", "timeout": "TIMEOUT", "skipped": "SKIP"}
	for _, t := range res.Tests {
		line := label[t.Status] + " " + t.Name
		if t.Status != "pass" && t.Status != "skipped" {
			line += ": " + humanDetail(t, srcs)
		}
		_, _ = fmt.Fprintln(w, line)
	}
	s := res.Summary
	_, _ = fmt.Fprintf(w, "%d passed, %d failed, %d mismatched, %d timed out, %d skipped\n", s.Pass, s.Fail, s.Mismatch, s.Timeout, s.Skipped)
}

// humanDetail names the first run that disagrees with the first one or
// misses the expectation.
func humanDetail(t testrun.TestResult, srcs []*source.File) string {
	pick := t.Runs[0]
	for _, r := range t.Runs {
		if r.Outcome == "timeout" || !testrun.Agree(t.Runs[0], r) {
			pick = r
			break
		}
	}
	s := pick.Engine + "/" + pick.Opt + " " + pick.Outcome
	switch pick.Outcome {
	case "trap":
		s += " " + pick.Trap.Kind + " at " + position(pick.Trap.File, pick.Trap.Span.Start, srcs)
	case "exit", "crash":
		s += " " + strconv.Itoa(pick.Status)
	}
	if t.Status == "mismatch" {
		s += " (" + t.Runs[0].Engine + "/" + t.Runs[0].Opt + " " + t.Runs[0].Outcome + ")"
	} else if t.Expect.Outcome == "trap" {
		s += ", expected trap " + t.Expect.Kind
	}
	return s
}

// position renders file:line:col for a byte offset of a program file.
func position(file string, off int, srcs []*source.File) string {
	for _, f := range srcs {
		if f.Name == file && off <= len(f.Bytes) {
			line := bytes.Count(f.Bytes[:off], []byte("\n")) + 1
			col := off - (bytes.LastIndexByte(f.Bytes[:off], '\n') + 1) + 1
			return fmt.Sprintf("%s:%d:%d", file, line, col)
		}
	}
	return fmt.Sprintf("%s@%d", file, off)
}
