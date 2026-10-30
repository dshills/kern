package testrun

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

func srcs(files map[string]string) []*source.File {
	var out []*source.File
	for _, n := range []string{"a.kern", "a_test.kern", "b_test.kern"} {
		if s, ok := files[n]; ok {
			out = append(out, &source.File{Name: n, Bytes: []byte(s)})
		}
	}
	return out
}

func find(t *testing.T, files map[string]string) ([]Test, []diag.Diagnostic) {
	t.Helper()
	ss := srcs(files)
	prog := resolve.Resolve(ss)
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %v", prog.Diags[0].Message)
	}
	return Find(prog, ss)
}

func codesOf(ds []diag.Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, string(d.Code))
	}
	return strings.Join(out, ",")
}

func TestFindOrderAndExpectations(t *testing.T) {
	tests, ds := find(t, map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return 0i32 }\nfunc test_not_in_test_file() {}\n",
		"b_test.kern": "module t\n// a plain comment\n//! expect: trap bounds\nfunc test_b() {}\n",
		"a_test.kern": "module t\nfunc helper() -> u64 { return 1u64 }\n//! phase: 3\n//! expect: return\n// trailing words\nfunc test_z() {}\nfunc test_a() {}\n\n//! expect: trap overflow\n\nfunc test_gap() {}\n",
	})
	if len(ds) > 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	var got []string
	for _, x := range tests {
		got = append(got, x.Name+"="+x.Expect.Outcome+x.Expect.Kind)
	}
	want := "t.test_z=return t.test_a=return t.test_gap=return t.test_b=trapbounds"
	if strings.Join(got, " ") != want {
		t.Fatalf("tests %v\nwant %s", got, want)
	}
	if tests[0].File != "a_test.kern" || tests[0].Span.Start == 0 {
		t.Errorf("location: %+v", tests[0])
	}
}

func TestFindDirectiveErrors(t *testing.T) {
	_, ds := find(t, map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return 0i32 }\n",
		"a_test.kern": "module t\n" +
			"//! expect: trap nonsense\nfunc test_kind() {}\n" +
			"//! expect: maybe\nfunc test_word() {}\n" +
			"//! expect: return\n//! expect: trap bounds\nfunc test_two() {}\n" +
			"//! expect: return\nfunc helper() {}\n",
	})
	if got := codesOf(ds); got != "TEST_DIRECTIVE,TEST_DIRECTIVE,TEST_DIRECTIVE,TEST_DIRECTIVE" {
		t.Fatalf("got %s: %v", got, ds)
	}
	for _, d := range ds {
		if d.File != "a_test.kern" || d.Span.End <= d.Span.Start {
			t.Errorf("located at the directive line: %+v", d)
		}
	}
}

func TestFindSignatures(t *testing.T) {
	_, ds := find(t, map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return 0i32 }\n",
		"a_test.kern": "module t\n" +
			"func test_param(x: u64) {}\n" +
			"func test_generic[T]() {}\n" +
			"func test_result() -> u64 { return 1u64 }\n" +
			"extern \"C\" func test_extern()\n" +
			"func test_ok() {}\n",
	})
	if got := codesOf(ds); got != "TEST_SIGNATURE,TEST_SIGNATURE,TEST_SIGNATURE,TEST_SIGNATURE" {
		t.Fatalf("got %s: %v", got, ds)
	}
}

func TestClassify(t *testing.T) {
	rec := []byte("noise\n{\"trap\":{\"kind\":\"bounds\",\"file\":\"a.kern\",\"span\":{\"start\":3,\"end\":7},\"thread\":0}}\n")
	cases := []struct {
		status             int
		timedOut, signaled bool
		stderr             []byte
		want               string
	}{
		{0, false, false, nil, "return"},
		{134, false, false, rec, "trap"},
		{134, false, false, []byte("abort\n"), "crash"},
		{139, false, true, nil, "crash"},
		{3, false, false, nil, "exit"},
		{-1, true, false, rec, "timeout"},
	}
	for _, c := range cases {
		r := Classify(c.status, c.timedOut, c.signaled, []byte("out"), c.stderr, time.Millisecond)
		if r.Outcome != c.want {
			t.Errorf("status %d timedOut %v signaled %v: %s, want %s", c.status, c.timedOut, c.signaled, r.Outcome, c.want)
		}
	}
	r := Classify(134, false, false, nil, rec, 0)
	if r.Trap == nil || r.Trap.Kind != "bounds" || r.Trap.File != "a.kern" || r.Trap.Span != (diag.Span{Start: 3, End: 7}) {
		t.Errorf("record: %+v", r.Trap)
	}
	big := make([]byte, MaxStdout+10)
	for i := range big {
		big[i] = 'x'
	}
	big[0] = 0xff
	r = Classify(0, false, false, big, nil, 0)
	if !r.StdoutTruncated || len(r.Stdout) != MaxStdout+2 || !strings.HasPrefix(r.Stdout, "�") {
		t.Errorf("truncation and UTF-8 repair: truncated=%v len=%d", r.StdoutTruncated, len(r.Stdout))
	}
}

func trapRun(kind string, file string, start int, out string) Run {
	return Classify(134, false, false, []byte(out), []byte(`{"trap":{"kind":"`+kind+`","file":"`+file+`","span":{"start":`+strconv.Itoa(start)+`,"end":`+strconv.Itoa(start+1)+`},"thread":7}}`), 0)
}

func TestAgree(t *testing.T) {
	base := trapRun("bounds", "a.kern", 3, "x")
	if !Agree(base, trapRun("bounds", "a.kern", 3, "x")) {
		t.Error("identical runs must agree (the thread is not compared)")
	}
	for name, other := range map[string]Run{
		"kind":   trapRun("overflow", "a.kern", 3, "x"),
		"file":   trapRun("bounds", "b.kern", 3, "x"),
		"span":   trapRun("bounds", "a.kern", 4, "x"),
		"stdout": trapRun("bounds", "a.kern", 3, "y"),
	} {
		if Agree(base, other) {
			t.Errorf("runs differing in %s must disagree", name)
		}
	}
	if !Agree(trapRun("stack", "a.kern", 3, ""), trapRun("stack", "a.kern", 5, "")) {
		t.Error("stack spans are not compared (ADR-012)")
	}
	if !Agree(trapRun("stack", "lib/lib.kern", 3, ""), trapRun("stack", "app.kern", 0, "")) {
		t.Error("stack files are not compared (specs/runtime-trap-file R7)")
	}
	if Agree(trapRun("bounds", "lib/lib.kern", 3, ""), trapRun("bounds", "app.kern", 3, "")) {
		t.Error("the file of any other kind is compared")
	}
	e3 := Classify(3, false, false, nil, nil, 0)
	e4 := Classify(4, false, false, nil, nil, 0)
	if Agree(e3, e4) || !Agree(e3, Classify(3, false, false, nil, nil, 0)) {
		t.Error("exit statuses are compared")
	}
	if Agree(Classify(0, false, false, nil, nil, 0), e3) {
		t.Error("outcomes are compared")
	}
}

func TestStatus(t *testing.T) {
	ret := Classify(0, false, false, nil, nil, 0)
	tb := trapRun("bounds", "a.kern", 3, "")
	to := Classify(-1, true, false, nil, nil, 0)
	cases := []struct {
		e    Expect
		runs []Run
		want string
	}{
		{Expect{"return", ""}, nil, "skipped"},
		{Expect{"return", ""}, []Run{ret, ret}, "pass"},
		{Expect{"trap", "bounds"}, []Run{tb, tb}, "pass"},
		{Expect{"trap", "bounds"}, []Run{ret, ret}, "fail"},
		{Expect{"trap", "overflow"}, []Run{tb, tb}, "fail"},
		{Expect{"return", ""}, []Run{tb}, "fail"},
		{Expect{"return", ""}, []Run{ret, tb}, "mismatch"},
		{Expect{"return", ""}, []Run{ret, tb, to}, "timeout"},
	}
	for i, c := range cases {
		if got := Status(c.e, c.runs); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

// A native build that fails is an internal error naming the build, and
// nothing runs (R11).
func TestExecuteNativeBuildFailure(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "clang")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho broken >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ss := srcs(map[string]string{
		"a.kern":      "module t\nfunc main() -> i32 { return 0i32 }\n",
		"a_test.kern": "module t\nfunc test_a() { expect(true) }\n",
	})
	res, ds, err := Execute(ss, Options{Configs: []Config{{"native", "debug"}}, Timeout: 5 * time.Second})
	var ie *InternalError
	if !errors.As(err, &ie) || !strings.Contains(err.Error(), "native build (debug)") || res != nil || len(ds) > 0 {
		t.Fatalf("res=%v ds=%v err=%v", res, ds, err)
	}
}

// The interpreter configurations run in process: a timed-out run reports
// what it had written, and its later writes are discarded (R8).
func TestExecuteInterpTimeout(t *testing.T) {
	ss := srcs(map[string]string{
		"a.kern":      "module t\nfunc main() -> i32 { return 0i32 }\n",
		"a_test.kern": "module t\nfunc test_loop() {\n    print_str(\"start\")\n    var i: u64 = 0u64\n    while true { i = wrapping_add(i, 1u64) }\n}\nfunc test_ok() { expect(true) }\n",
	})
	res, ds, err := Execute(ss, Options{Configs: []Config{{"interp", "debug"}}, Timeout: time.Second})
	if err != nil || len(ds) > 0 {
		t.Fatalf("ds=%v err=%v", ds, err)
	}
	if len(res.Tests) != 2 || res.Tests[0].Status != "timeout" || res.Tests[1].Status != "pass" {
		t.Fatalf("statuses: %+v", res.Tests)
	}
	r := res.Tests[0].Runs[0]
	if r.Outcome != "timeout" || r.Status != -1 || r.Stdout != "start" {
		t.Fatalf("timed-out run: %+v", r)
	}
	if res.Summary != (Summary{Pass: 1, Timeout: 1}) || res.OK() {
		t.Fatalf("summary %+v ok=%v", res.Summary, res.OK())
	}
}

// lowerTests compiles test sources the way Execute does, for one level.
func lowerTests(t *testing.T, files map[string]string, optLevel string) (*ir.Module, []Test) {
	t.Helper()
	ss := srcs(files)
	prog := resolve.ResolveFor(ss, optLevel == "release")
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %v", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %v", ds[0].Message)
	}
	tests, ds := Find(prog, ss)
	if len(ds) > 0 {
		t.Fatalf("find: %v", ds)
	}
	var decls []*ast.FuncDecl
	for _, x := range tests {
		decls = append(decls, x.Decl)
	}
	mod, lds := lower.LowerWith(prog, lower.Options{Tests: decls})
	if len(lds) > 0 {
		t.Fatalf("lower: %v", lds[0].Message)
	}
	if err := opt.Run(mod, opt.OptionsFor(optLevel)); err != nil {
		t.Fatal(err)
	}
	return mod, tests
}

// Stop ends a looping interpreter run, on the main thread and on a
// spawned one, within the grace period (prism: a timed-out run must not
// keep the CPU).
func TestInterpStop(t *testing.T) {
	mod, tests := lowerTests(t, map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return 0i32 }\n",
		"a_test.kern": "module t\n" +
			"func spin(n: u64) -> u64 {\n    var i: u64 = n\n    while true { i = wrapping_add(i, 1u64) }\n    return i\n}\n" +
			"func joined() -> Result[u64, SpawnErr] {\n    with s := thread.scope() {\n        h := s.spawn(spin, 1u64)?\n        return Ok(h.get())\n    }\n}\n" +
			"func test_main_loops() { print_u64(spin(0u64)) }\n" +
			"func test_thread_loops() {\n    match joined() {\n        Ok(v) => print_u64(v)\n        Err(e) => {}\n    }\n}\n",
	}, "debug")
	for _, x := range tests {
		it := interp.New(mod, x.File, io.Discard, io.Discard)
		done := make(chan int, 1)
		go func() { done <- it.RunEntry(x.Name) }()
		time.Sleep(50 * time.Millisecond)
		it.Stop()
		select {
		case st := <-done:
			if st != interp.StoppedStatus {
				t.Errorf("%s: status %d, want %d", x.Name, st, interp.StoppedStatus)
			}
		case <-time.After(stopGrace):
			t.Errorf("%s: still running after Stop", x.Name)
		}
	}
}

// The capture keeps MaxStdout+1 bytes of the head and compares the whole
// output by length and digest, so outputs that differ only past the
// response's prefix still disagree.
func TestCaptureBounded(t *testing.T) {
	big := []byte(strings.Repeat("x", 3*MaxStdout))
	c := newCapture(0)
	for i := 0; i < 3; i++ {
		_, _ = c.Write(big[i*MaxStdout : (i+1)*MaxStdout])
	}
	o := c.output()
	if len(o.head) != MaxStdout+1 || o.n != int64(len(big)) {
		t.Fatalf("head %d n %d", len(o.head), o.n)
	}
	other := append([]byte(nil), big...)
	other[len(other)-1] = 'y'
	a := classify(0, false, false, o, nil, 0)
	b := classify(0, false, false, outputOf(other), nil, 0)
	if Agree(a, b) || !Agree(a, classify(0, false, false, outputOf(big), nil, 0)) {
		t.Error("agreement must compare the whole output")
	}
	if !a.StdoutTruncated || len(a.Stdout) != MaxStdout {
		t.Errorf("rendered %d bytes truncated=%v", len(a.Stdout), a.StdoutTruncated)
	}
	tail := newCapture(8)
	for i := 0; i < 100; i++ {
		_, _ = tail.Write([]byte("0123456789"))
	}
	if got := tail.tailBytes(); len(got) > 16 || !strings.HasSuffix(string(got), "89") {
		t.Errorf("tail %q", got)
	}
	huge := newCapture(8)
	_, _ = huge.Write([]byte(strings.Repeat("a", 1000) + "01234567"))
	if got := string(huge.tailBytes()); got != "01234567" {
		t.Errorf("a write longer than the tail keeps its last bytes only: %q", got)
	}
	tail.Close()
	_, _ = tail.Write([]byte("late"))
	if strings.HasSuffix(string(tail.tailBytes()), "late") {
		t.Error("a closed capture keeps no later write")
	}
}

// The native test executable runs only a test its argument names exactly
// (prism: atoi would run test 0 for "" or "abc").
func TestNativeIndexStrict(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("clang not found")
	}
	files := map[string]string{
		"a.kern":      "module t\nfunc main() -> i32 { return 0i32 }\n",
		"a_test.kern": "module t\nfunc test_a() { print_str(\"a\") }\nfunc test_b() { print_str(\"b\") }\n",
	}
	mod, _ := lowerTests(t, files, "debug")
	exe := filepath.Join(t.TempDir(), "tests")
	if err := native.BuildModule(mod, srcs(files), exe, native.Options{Opt: "debug"}); err != nil {
		t.Fatal(err)
	}
	for arg, want := range map[string]string{"0": "a", "1": "b"} {
		out, err := exec.Command(exe, arg).Output()
		if err != nil || string(out) != want {
			t.Errorf("%q: %q %v", arg, out, err)
		}
	}
	for _, arg := range []string{"", "abc", "1junk", " 1", "-1", "+1", "2", "99999999999999999999999"} {
		out, err := exec.Command(exe, arg).Output()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 || len(out) != 0 {
			t.Errorf("%q: exit %v output %q", arg, err, out)
		}
	}
	if out, err := exec.Command(exe).Output(); len(out) != 0 || err == nil {
		t.Errorf("no argument: %q %v", out, err)
	}
}

// TestStaticDiags: the test diagnostics from parsing alone equal Find's on
// the same program, and a test file that does not parse contributes none
// (specs/editor-nav R6).
func TestStaticDiags(t *testing.T) {
	files := map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return 0i32 }\n",
		"a_test.kern": "module t\nfunc test_param(x: u64) {}\n//! expect: maybe\nfunc test_word() {}\n" +
			"//! expect: return\nfunc helper() {}\nfunc test_ok() {}\n",
	}
	_, want := find(t, files)
	got := StaticDiags(srcs(files))
	if codesOf(got) != codesOf(want) || len(got) != 3 {
		t.Fatalf("StaticDiags %s, Find %s", codesOf(got), codesOf(want))
	}
	for i := range got {
		if got[i].File != want[i].File || got[i].Span != want[i].Span || got[i].Message != want[i].Message {
			t.Errorf("diagnostic %d: %+v, Find's %+v", i, got[i], want[i])
		}
	}
	broken := map[string]string{"a.kern": files["a.kern"], "a_test.kern": "module t\nfunc test_x( {\n"}
	if ds := StaticDiags(srcs(broken)); len(ds) != 0 {
		t.Errorf("a test file that does not parse: %v", ds)
	}
}
