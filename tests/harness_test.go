// Package tests drives every fixture under tests/ through the checker and
// verifies the diagnostic registry's coverage rules (PLAN.md §0).
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// Phase is the current implementation phase; fixtures whose executable phase
// is later are check-only.
const Phase = 2

type header struct {
	phase     int
	expect    string // accept | reject | trap | output
	code      string
	rule      string
	twin      string
	testHooks bool
	noRecord  bool   // trap-record: none — a custom handler writes no default record
	cfile     string // c: the C counterpart of an FFI fixture (native-only execution)
	native    bool   // native-only: true — the interpreter cannot execute it (inline assembly)
	env       string // env: freestanding — run under the freestanding emulation and build bare-metal
	tsan      string // tsan: clean | race — build natively with ThreadSanitizer and expect no report, or a report
}

func parseHeader(t *testing.T, f *source.File) header {
	t.Helper()
	h := header{phase: -1}
	for _, line := range f.Headers() {
		key, val, _ := strings.Cut(line, ":")
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "phase":
			n := 0
			for _, ch := range val {
				n = n*10 + int(ch-'0')
			}
			h.phase = n
		case "expect":
			parts := strings.Fields(val)
			if len(parts) == 0 {
				t.Fatalf("%s: empty expect", f.Name)
			}
			h.expect = parts[0]
			for _, p := range parts[1:] {
				if strings.HasPrefix(p, "rule=") {
					h.rule = strings.TrimPrefix(p, "rule=")
				} else {
					h.code = p
				}
			}
		case "twin":
			h.twin = val
		case "test-hooks":
			h.testHooks = val == "true"
		case "trap-record":
			h.noRecord = val == "none"
		case "c":
			h.cfile = val
		case "native-only":
			h.native = val == "true"
		case "env":
			h.env = val
		case "tsan":
			h.tsan = val
		}
	}
	return h
}

func checkFiles(fs []*source.File) []diag.Diagnostic {
	prog := resolve.Resolve(fs)
	if len(prog.Diags) > 0 {
		return prog.Diags
	}
	return check.Check(prog)
}

// fixtures lists fixture paths: single .kern files, and directories holding a
// multi-file fixture (its header is in the alphabetically first file).
func fixtures(t *testing.T) []string {
	t.Helper()
	var out []string
	dirs := []string{"positive", "negative", "adversarial", "trap", "run", "ffi", "freestanding", "concurrency", filepath.Join("concurrency", "litmus")}
	listed := map[string]bool{}
	for _, dir := range dirs {
		listed[dir] = true
	}
	for _, dir := range dirs {
		m, _ := filepath.Glob(filepath.Join(dir, "*.kern"))
		out = append(out, m...)
		ds, _ := filepath.Glob(filepath.Join(dir, "*"))
		for _, d := range ds {
			// a subdirectory is a multi-file fixture unless it is a fixture directory itself
			if st, err := os.Stat(d); err == nil && st.IsDir() && !listed[d] {
				out = append(out, d)
			}
		}
	}
	ex, _ := filepath.Glob(filepath.Join("..", "examples", "*", "*.kern"))
	out = append(out, ex...)
	sort.Strings(out)
	return out
}

// loadFixture loads a single-file or directory fixture.
func loadFixture(t *testing.T, path string) (*source.File, []*source.File) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() {
		f, err := source.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return f, []*source.File{f}
	}
	var names []string
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".kern") {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	var files []*source.File
	for _, n := range names {
		f, err := source.Load(n)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files[0], files
}

func TestFixtures(t *testing.T) {
	for _, path := range fixtures(t) {
		path := path
		t.Run(path, func(t *testing.T) {
			f, all := loadFixture(t, path)
			h := parseHeader(t, f)
			ds := checkFiles(all)
			switch h.expect {
			case "accept", "output", "trap":
				if h.phase < 1 {
					t.Fatalf("accept/trap fixture must declare phase >= 1")
				}
				if len(ds) != 0 {
					t.Fatalf("expected acceptance, got %d diagnostics:\n%s", len(ds), render(ds))
				}
				if h.phase <= Phase {
					execute(t, path, all, h)
				}
			case "reject":
				if h.phase != -1 {
					t.Fatalf("reject fixtures carry no phase line")
				}
				if len(ds) != 1 {
					t.Fatalf("expected exactly one diagnostic %s, got %d:\n%s", h.code, len(ds), render(ds))
				}
				if string(ds[0].Code) != h.code {
					t.Fatalf("expected %s, got %s: %s", h.code, ds[0].Code, ds[0].Message)
				}
				if h.rule != "" && ds[0].Rule != h.rule {
					t.Fatalf("expected rule %s, got %s", h.rule, ds[0].Rule)
				}
			default:
				t.Fatalf("unknown expect %q", h.expect)
			}
			if h.twin != "" {
				twin := filepath.Join(filepath.Dir(path), h.twin+".kern")
				if _, err := os.Stat(twin); err != nil {
					if _, err2 := os.Stat(filepath.Join(filepath.Dir(path), h.twin)); err2 != nil {
						t.Fatalf("twin %s does not exist", twin)
					}
				}
			}
		})
	}
}

func render(ds []diag.Diagnostic) string {
	var sb strings.Builder
	for _, d := range ds {
		sb.WriteString(diag.Render(d))
		sb.WriteString("\n")
	}
	return sb.String()
}

var codeRe = regexp.MustCompile("`([A-Z]+(?:_[A-Z]+)+)`")

// TestRegistryInventory checks level 1: every code named in the spec and the
// language reference is registered (an omitted registry entry cannot evade
// coverage).
func TestRegistryInventory(t *testing.T) {
	seen := map[string]bool{}
	for _, doc := range []string{"../specs/initial/AI_Native_Systems_Language_SPEC.md", "../docs/ownership.md", "../spec/types.md"} {
		b, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range codeRe.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
	}
	skip := map[string]bool{"TRAP_INFO": true, "OWN_": true, "INIT_": true, "ACC_": true, "SEND_": true, "SHARE_": true, "UNSAFE_": true, "EDIT_": true, "API_": true}
	for code := range seen {
		if skip[code] || strings.HasSuffix(code, "_") {
			continue
		}
		if !diag.Valid(diag.Code(code)) {
			t.Errorf("code %s appears in the spec or reference but is not registered", code)
		}
	}
}

// TestRegistryCoverage checks levels 2 and 3: every registered lang code
// introduced in this phase or earlier has a reject fixture with an accepting
// twin, and every api/cli/internal code has its test file.
func TestRegistryCoverage(t *testing.T) {
	covered := map[string]bool{}
	for _, path := range fixtures(t) {
		f, _ := loadFixture(t, path)
		h := parseHeader(t, f)
		if h.expect != "reject" || h.twin == "" {
			continue
		}
		twin := filepath.Join(filepath.Dir(path), h.twin+".kern")
		if _, err := os.Stat(twin); err != nil {
			twin = filepath.Join(filepath.Dir(path), h.twin)
		}
		tf, _ := loadFixture(t, twin)
		if th := parseHeader(t, tf); th.expect != "accept" {
			t.Errorf("%s: twin %s is not an accepting fixture", path, twin)
			continue
		}
		covered[h.code] = true
	}
	// Codes whose rejecting input cannot be a committed fixture file are
	// covered by generated tests in this package; each names its test.
	generated := map[string]string{"TYPE_TOO_MANY_VARIANTS": "TestTooManyVariants"}
	for code := range generated {
		covered[code] = true
	}
	for code, info := range diag.Registry {
		if info.Phase > Phase {
			continue
		}
		switch info.Category {
		case diag.CatLang:
			if !covered[string(code)] {
				t.Errorf("lang code %s (phase %d) has no reject fixture with an accepting twin", code, info.Phase)
			}
		case diag.CatAPI, diag.CatEdit:
			if _, err := os.Stat(filepath.Join("api", strings.ToLower(string(code))+".json")); err != nil {
				t.Errorf("api code %s has no request fixture tests/api/%s.json", code, strings.ToLower(string(code)))
			}
		}
	}
}

// TestTooManyVariants generates a union with 65,537 variants in memory (the
// fixture is too large to commit) and its accepting twin with 65,536.
func TestTooManyVariants(t *testing.T) {
	gen := func(n int) *source.File {
		var sb strings.Builder
		sb.WriteString("module m\nunion Big {\n")
		for i := 0; i < n; i++ {
			sb.WriteString("V")
			sb.WriteString(strings.TrimLeft(strings.Repeat("0", 6-len(itoa(i)))+itoa(i), "x"))
			sb.WriteString("\n")
		}
		sb.WriteString("}\n")
		return &source.File{Name: "big.kern", Bytes: []byte(sb.String())}
	}
	ds := checkFiles([]*source.File{gen(65537)})
	if len(ds) != 1 || ds[0].Code != "TYPE_TOO_MANY_VARIANTS" {
		t.Fatalf("expected TYPE_TOO_MANY_VARIANTS, got %v", render(ds))
	}
	if ds := checkFiles([]*source.File{gen(65536)}); len(ds) != 0 {
		t.Fatalf("twin rejected: %s", render(ds))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// execute runs an accepted fixture that has a main function through the
// interpreter and checks its expected output or trap (spec §7 Testing).
func execute(t *testing.T, path string, files []*source.File, h header) {
	t.Helper()
	prog := resolve.Resolve(files)
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %s", ds[0].Message)
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validate: %v", errs[0])
	}
	if mod.Entry == "" {
		if h.expect != "accept" {
			t.Fatalf("%s fixture has no main", h.expect)
		}
		return
	}
	if h.cfile != "" || h.native {
		// FFI fixtures execute natively with their C counterpart (the
		// interpreter bridge is deferred, docs/reports/phase-2.md); inline
		// assembly fixtures are native-only by nature
		nativeOnly(t, path, files, h)
		return
	}
	var out, errb bytes.Buffer
	it := interp.New(mod, path, &out, &errb)
	it.TestHooks = h.testHooks
	if h.env == "freestanding" {
		freestandingRun(t, path, files, h, mod)
		return
	}
	status := it.Run()
	switch h.expect {
	case "trap":
		if status != 134 {
			t.Fatalf("expected trap %s (status 134), got status %d, stderr %q", h.code, status, errb.String())
		}
		if h.noRecord {
			if errb.Len() != 0 {
				t.Fatalf("expected no trap record (custom handler), got %q", errb.String())
			}
			break
		}
		var rec struct {
			Trap struct {
				Kind string `json:"kind"`
			} `json:"trap"`
		}
		line := bytes.SplitN(errb.Bytes(), []byte("\n"), 2)[0]
		if err := json.Unmarshal(line, &rec); err != nil || rec.Trap.Kind != h.code {
			t.Fatalf("expected trap record of kind %s, got %q", h.code, errb.String())
		}
		if bytes.Count(bytes.TrimSpace(errb.Bytes()), []byte("\n")) != 0 {
			t.Fatalf("expected exactly one trap record, got %q", errb.String())
		}
	case "output", "accept":
		if status == 134 {
			t.Fatalf("unexpected trap: %s", errb.String())
		}
		name := h.code
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(path), ".kern")
		}
		want, err := os.ReadFile(filepath.Join(filepath.Dir(path), name+".out"))
		if err != nil && h.expect == "output" {
			t.Fatalf("missing expected-output file for %s", name)
		}
		if err == nil && !bytes.Equal(out.Bytes(), want) {
			t.Fatalf("output mismatch:\n--- want\n%s\n--- got\n%s", want, out.String())
		}
	}
	if h.tsan == "race" {
		// a deliberate-race twin: only ThreadSanitizer judges it
		tsanRun(t, files, h)
		return
	}
	// every executable fixture also runs natively, with or without an
	// expected-output file
	nativeRun(t, files, h, out.Bytes(), errb.Bytes(), status)
	if h.tsan == "clean" {
		tsanRun(t, files, h)
	}
}

// freestandingRun executes a freestanding fixture in the interpreter's
// freestanding emulation (console device, halt line) against its expected
// console output, then builds the bare-metal image: with ld.lld and QEMU on
// the PATH the image runs under `qemu-system-aarch64 -M virt -cpu cortex-a72
// -serial stdio -display none -no-reboot` and its console must match too;
// otherwise the build is an object archive and only compilation is checked.
func freestandingRun(t *testing.T, path string, files []*source.File, h header, mod *ir.Module) {
	t.Helper()
	var out, errb bytes.Buffer
	it := interp.New(mod, path, &out, &errb)
	it.TestHooks = h.testHooks
	it.Freestanding = true
	status := it.Run()
	name := h.code
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".kern")
	}
	want, err := os.ReadFile(filepath.Join(filepath.Dir(path), name+".out"))
	if err != nil {
		t.Fatalf("missing expected console file for %s", name)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("console mismatch (status %d):\n--- want\n%s\n--- got\n%s", status, want, out.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("freestanding emulation wrote to standard error: %q", errb.String())
	}
	if !native.Available() {
		t.Log("clang not available: freestanding build skipped")
		return
	}
	img := filepath.Join(t.TempDir(), "image")
	ds, err := native.Build(files, img, native.Options{Freestanding: true})
	if err != nil {
		t.Fatalf("freestanding build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("freestanding build diagnostics: %s", render(ds))
	}
	if !native.Linkable() || !native.QEMUAvailable() {
		t.Log("ld.lld or qemu-system-aarch64 not available: freestanding image compiled, not run")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "qemu-system-aarch64", "-M", "virt", "-cpu", "cortex-a72", "-serial", "stdio", "-display", "none", "-no-reboot", "-kernel", img)
	var qout, qerr bytes.Buffer
	cmd.Stdout = &qout
	cmd.Stderr = &qerr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("QEMU did not exit within the budget")
	}
	if err != nil {
		// PSCI SYSTEM_OFF ends QEMU with status 0; anything else is a guest or emulator failure
		t.Fatalf("QEMU terminated abnormally: %v\n%s", err, qerr.String())
	}
	if !bytes.Equal(bytes.ReplaceAll(qout.Bytes(), []byte("\r\n"), []byte("\n")), want) {
		t.Fatalf("QEMU console mismatch:\n--- want\n%s\n--- got\n%s", want, qout.String())
	}
}

// tsanRun builds the fixture with ThreadSanitizer and checks that a report
// appears exactly when the fixture declares a deliberate race (PLAN.md 3.3).
func tsanRun(t *testing.T, files []*source.File, h header) {
	t.Helper()
	if !native.Available() {
		t.Skip("clang not available")
	}
	if !tsanSupported(t) {
		t.Skip("ThreadSanitizer is not supported by this toolchain")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	ds, err := native.Build(files, exe, native.Options{TSan: true, TestHooks: h.testHooks})
	if err != nil {
		t.Fatalf("ThreadSanitizer build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("native build diagnostics: %s", render(ds))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("sanitized program did not finish within the budget")
	}
	race := strings.Contains(errb.String(), "WARNING: ThreadSanitizer: data race")
	if strings.Contains(errb.String(), "FATAL: ThreadSanitizer") {
		t.Fatalf("ThreadSanitizer failed to start:\n%s", errb.String())
	}
	switch h.tsan {
	case "race":
		if !race {
			t.Fatalf("deliberate race not reported by ThreadSanitizer; stderr %q", errb.String())
		}
	case "clean":
		if race {
			t.Fatalf("ThreadSanitizer report on a race-free fixture:\n%s", errb.String())
		}
		if runErr != nil {
			t.Fatalf("sanitized program failed: %v, stderr %q", runErr, errb.String())
		}
	}
}

var tsanProbe struct {
	once sync.Once
	ok   bool
}

// tsanSupported probes the toolchain once with a trivial sanitized C program.
func tsanSupported(t *testing.T) bool {
	t.Helper()
	tsanProbe.once.Do(func() {
		dir, err := os.MkdirTemp("", "kern-tsan-probe-")
		if err != nil {
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		src := filepath.Join(dir, "p.c")
		if err := os.WriteFile(src, []byte("int main(void){return 0;}\n"), 0o644); err != nil {
			return
		}
		exe := filepath.Join(dir, "p")
		if err := exec.Command("clang", "-fsanitize=thread", "-o", exe, src).Run(); err != nil {
			return
		}
		tsanProbe.ok = exec.Command(exe).Run() == nil
	})
	return tsanProbe.ok
}

// nativeOnly runs an FFI fixture natively against its expected output.
func nativeOnly(t *testing.T, path string, files []*source.File, h header) {
	t.Helper()
	if !native.Available() {
		t.Skip("clang not available")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	var extra []string
	if h.cfile != "" {
		extra = append(extra, filepath.Join(filepath.Dir(path), h.cfile))
	}
	ds, err := native.Build(files, exe, native.Options{ExtraSources: extra, TestHooks: h.testHooks})
	if err != nil {
		t.Fatalf("native build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("native build diagnostics: %s", render(ds))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("native run: %v, stderr %q", err, errb.String())
	}
	name := h.code
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".kern")
	}
	want, err := os.ReadFile(filepath.Join(filepath.Dir(path), name+".out"))
	if err != nil {
		t.Fatalf("missing expected-output file for %s", name)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("output mismatch:\n--- want\n%s\n--- got\n%s", want, out.String())
	}
}

// nativeRun builds the fixture natively and compares the run with the
// interpreter's: exit status, standard output byte for byte, and the trap
// record's kind and span (PLAN.md 2.1 differential testing). Stack traps
// compare by kind only: the native fault handler has no source span
// (ADR-012). Without clang the native half is skipped and logged.
func nativeRun(t *testing.T, files []*source.File, h header, wantOut, wantErr []byte, wantStatus int) {
	t.Helper()
	if !native.Available() {
		t.Log("clang not available: native differential run skipped")
		return
	}
	exe := filepath.Join(t.TempDir(), "prog")
	ds, err := native.Build(files, exe, native.Options{TestHooks: h.testHooks})
	if err != nil {
		t.Fatalf("native build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("native build diagnostics: %s", render(ds))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	status := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("native run: %v", err)
		}
		ws, ok := ee.Sys().(syscall.WaitStatus)
		if !ok {
			t.Fatalf("native run: %v", err)
		}
		switch {
		case ws.Signaled() && ws.Signal() == syscall.SIGABRT:
			status = 134
		case ws.Signaled():
			t.Fatalf("native program died by signal %v, stderr %q", ws.Signal(), errb.String())
		default:
			status = ws.ExitStatus()
		}
	}
	if status != wantStatus {
		t.Fatalf("native status %d, interpreter status %d; native stderr %q", status, wantStatus, errb.String())
	}
	if !bytes.Equal(out.Bytes(), wantOut) {
		t.Fatalf("native output differs from the interpreter's:\n--- interpreter\n%s\n--- native\n%s", wantOut, out.String())
	}
	compareTrapRecords(t, wantErr, errb.Bytes())
}

type trapRecord struct {
	Trap struct {
		Kind string `json:"kind"`
		Span struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"span"`
	} `json:"trap"`
}

func compareTrapRecords(t *testing.T, want, got []byte) {
	t.Helper()
	want = bytes.TrimSpace(want)
	got = bytes.TrimSpace(got)
	if len(want) == 0 || len(got) == 0 {
		if len(want) != len(got) {
			t.Fatalf("trap records differ:\n--- interpreter\n%s\n--- native\n%s", want, got)
		}
		return
	}
	var w, g trapRecord
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("interpreter record is not JSON: %q", want)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("native record is not JSON: %q", got)
	}
	if w.Trap.Kind != g.Trap.Kind {
		t.Fatalf("trap kind: interpreter %s, native %s", w.Trap.Kind, g.Trap.Kind)
	}
	if w.Trap.Kind != "stack" && w.Trap.Span != g.Trap.Span {
		t.Fatalf("trap span: interpreter %v, native %v", w.Trap.Span, g.Trap.Span)
	}
}
