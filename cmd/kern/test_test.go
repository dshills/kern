package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/source"
	"github.com/dshills/kern/compiler/testrun"
)

// inProject writes files into a fresh directory and makes it the working
// directory.
func inProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

// testResult decodes a kern test response's result.
func testResult(t *testing.T, out string) (diag.Response, testrun.Result) {
	t.Helper()
	resp := response(t, out)
	var res testrun.Result
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("result: %v\n%s", err, out)
	}
	return resp, res
}

func hasClang() bool { _, err := exec.LookPath("clang"); return err == nil }

func configsOf(res testrun.Result) string {
	var out []string
	for _, c := range res.Configurations {
		out = append(out, c.Engine+"/"+c.Opt)
	}
	return strings.Join(out, " ")
}

func byName(res testrun.Result) map[string]testrun.TestResult {
	out := map[string]testrun.TestResult{}
	for _, x := range res.Tests {
		out[x.Name] = x
	}
	return out
}

const mixedApp = "module app\nfunc twice(n: u64) -> u64 { return n * 2u64 }\nfunc main() -> i32 { return 0i32 }\n"

const mixedTests = `module app
thread_local hits: u64 = 0u64
func test_twice() {
    expect(twice(2u64) == 4u64)
}
//! expect: trap bounds
func test_index() {
    xs := [1u64, 2u64]
    var i: usize = 2usize
    print_u64(xs[i])
}
func test_false() {
    expect(twice(2u64) == 5u64)
}
//! expect: trap bounds
func test_returns_instead() {
    print_str("no trap")
}
func test_exits() {
    exit(3i32)
}
func test_identity_tag() {
    print_u64(u64(container_identity()))
}
func test_hits_a() {
    hits = hits + 1u64
    expect(hits == 1u64)
}
func test_hits_b() {
    hits = hits + 1u64
    expect(hits == 1u64)
}
`

// R6-R11: every status but timeout and skipped, in the default
// configurations, with the response's fields.
func TestKernTestMixed(t *testing.T) {
	inProject(t, map[string]string{"app.kern": mixedApp, "app_test.kern": mixedTests})
	code, out, errb := invoke(t, "", "test", "app.kern")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, errb)
	}
	resp, res := testResult(t, out)
	if resp.OK || resp.SchemaVersion != diag.SchemaVersion || len(resp.Diagnostics) != 0 {
		t.Fatalf("envelope: ok=%v version=%s diags=%v", resp.OK, resp.SchemaVersion, resp.Diagnostics)
	}
	nruns := 2
	if hasClang() {
		nruns = 4
		if configsOf(res) != "interp/debug interp/release native/debug native/release" || len(res.Skipped) != 0 {
			t.Fatalf("configurations %s skipped %v", configsOf(res), res.Skipped)
		}
	}
	want := map[string]string{
		"app.test_twice": "pass", "app.test_index": "pass", "app.test_false": "fail",
		"app.test_returns_instead": "fail", "app.test_exits": "fail", "app.test_identity_tag": "mismatch",
		"app.test_hits_a": "pass", "app.test_hits_b": "pass",
	}
	got := byName(res)
	var order []string
	for _, x := range res.Tests {
		order = append(order, x.Name)
		if len(x.Runs) != nruns {
			t.Errorf("%s: %d runs, want %d", x.Name, len(x.Runs), nruns)
		}
	}
	if strings.Join(order, " ") != "app.test_twice app.test_index app.test_false app.test_returns_instead app.test_exits app.test_identity_tag app.test_hits_a app.test_hits_b" {
		t.Errorf("order: %v", order)
	}
	for name, status := range want {
		if got[name].Status != status {
			t.Errorf("%s: %s, want %s (%+v)", name, got[name].Status, status, got[name].Runs)
		}
	}
	if res.Summary != (testrun.Summary{Pass: 4, Fail: 3, Mismatch: 1}) {
		t.Errorf("summary %+v", res.Summary)
	}
	// the failed expect: an explicit trap at the expect call of the test file
	f := got["app.test_false"]
	r := f.Runs[0]
	if r.Outcome != "trap" || r.Trap.Kind != "explicit" || r.Trap.File != "app_test.kern" || mixedTests[r.Trap.Span.Start:r.Trap.Span.End] != "expect(twice(2u64) == 5u64)" {
		t.Errorf("failed expect: %+v", r.Trap)
	}
	if f.Loc.File != "app_test.kern" || !strings.HasPrefix(mixedTests[f.Loc.Span.Start:], "func test_false") || f.Expect.Outcome != "return" {
		t.Errorf("loc and expect: %+v", f)
	}
	if e := got["app.test_exits"].Runs[0]; e.Outcome != "exit" || e.Status != 3 {
		t.Errorf("exit run: %+v", e)
	}
	if ix := got["app.test_index"]; ix.Expect != (testrun.Expect{Outcome: "trap", Kind: "bounds"}) || ix.Runs[0].Status != 134 {
		t.Errorf("expected trap: %+v", ix)
	}
	if ri := got["app.test_returns_instead"].Runs[0]; ri.Outcome != "return" || ri.Stdout != "no trap" || ri.StdoutTruncated {
		t.Errorf("returned run: %+v", ri)
	}
	// the human form: one line per test, the summary, the same exit
	code, human, _ := invoke(t, "", "test", "--human", "app.kern")
	if code != 1 || !strings.Contains(human, "PASS app.test_twice\n") || !strings.Contains(human, "4 passed, 3 failed, 1 mismatched, 0 timed out, 0 skipped\n") {
		t.Errorf("human (exit %d):\n%s", code, human)
	}
	if !regexp.MustCompile(`FAIL app.test_false: interp/debug trap explicit at app_test.kern:13:5\n`).MatchString(human) {
		t.Errorf("human failure line:\n%s", human)
	}
	if !strings.Contains(human, "FAIL app.test_returns_instead: interp/debug return, expected trap bounds\n") || !strings.Contains(human, "MISMATCH app.test_identity_tag: interp/release return (interp/debug return)\n") {
		t.Errorf("human lines:\n%s", human)
	}
	// R14: two responses are equal but for millis
	_, again, _ := invoke(t, "", "test", "app.kern")
	millis := regexp.MustCompile(`"millis":\d+`)
	if millis.ReplaceAllString(out, "") != millis.ReplaceAllString(again, "") {
		t.Error("two responses differ beyond millis")
	}
}

// R6, R12: flags select configurations and tests; bad values are refused.
func TestKernTestFlags(t *testing.T) {
	inProject(t, map[string]string{
		"kern.mod":      `{"entry": "app.kern"}`,
		"app.kern":      mixedApp,
		"app_test.kern": "module app\nfunc test_a() { expect(true) }\nfunc test_b() { expect(true) }\n",
	})
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--engine=interp"}, "interp/debug interp/release"},
		{[]string{"--opt=release", "--engine=interp"}, "interp/release"},
		{[]string{"--opt=debug"}, "interp/debug native/debug"},
	} {
		args := append([]string{"test"}, c.args...)
		code, out, _ := invoke(t, "", args...)
		_, res := testResult(t, out)
		want := c.want
		if !hasClang() {
			want = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(want, "native/debug", ""), "native/release", ""))
		}
		if code != 0 || configsOf(res) != want {
			t.Errorf("%v: exit %d configurations %q, want %q", c.args, code, configsOf(res), want)
		}
	}
	code, out, _ := invoke(t, "", "test", "--engine=interp", "--run=test_b$")
	if _, res := testResult(t, out); code != 0 || len(res.Tests) != 1 || res.Tests[0].Name != "app.test_b" {
		t.Errorf("--run selects one test: %s", out)
	}
	for _, bad := range []string{"--run=(", "--timeout=0", "--timeout=3601", "--timeout=1.5", "--timeout=x", "--opt=fast", "--engine=jit", "--env=freestanding", "--test-hooks", "--frob"} {
		if code, out, _ := invoke(t, "", "test", bad); code != 2 || firstCode(t, out) != "CLI_INVALID_FLAGS" {
			t.Errorf("%s: exit %d %s", bad, code, out)
		}
	}
	// without clang: native is skipped by default and refused when asked for
	t.Setenv("PATH", t.TempDir())
	code, out, _ = invoke(t, "", "test")
	if _, res := testResult(t, out); code != 0 || configsOf(res) != "interp/debug interp/release" || len(res.Skipped) != 1 || res.Skipped[0].Engine != "native" {
		t.Errorf("no clang: exit %d %s", code, out)
	}
	if code, out, _ := invoke(t, "", "test", "--engine=native"); code != 2 || firstCode(t, out) != "CLI_INVALID_FLAGS" {
		t.Errorf("--engine=native without clang: exit %d %s", code, out)
	}
}

func TestKernTestManifestFreestanding(t *testing.T) {
	inProject(t, map[string]string{"kern.mod": `{"entry": "app.kern", "env": "freestanding"}`, "app.kern": mixedApp})
	if code, out, _ := invoke(t, "", "test"); code != 2 || firstCode(t, out) != "CLI_INVALID_FLAGS" {
		t.Errorf("exit %d %s", code, out)
	}
}

// R3, R4: test diagnostics end the command before any run.
func TestKernTestDiagnostics(t *testing.T) {
	inProject(t, map[string]string{
		"app.kern": mixedApp,
		"app_test.kern": "module app\nfunc test_p(x: u64) {}\nfunc test_g[T]() {}\nfunc test_r() -> u64 { return 1u64 }\n" +
			"extern \"C\" func test_e()\n//! expect: trap nothing\nfunc test_d() {}\n//! expect: return\nfunc helper() {}\n",
	})
	code, out, _ := invoke(t, "", "test", "app.kern")
	resp := response(t, out)
	var got []string
	for _, d := range resp.Diagnostics {
		got = append(got, string(d.Code))
	}
	if code != 2 || resp.OK || resp.Result != nil || strings.Join(got, ",") != "TEST_SIGNATURE,TEST_SIGNATURE,TEST_SIGNATURE,TEST_SIGNATURE,TEST_DIRECTIVE,TEST_DIRECTIVE" {
		t.Errorf("exit %d codes %v\n%s", code, got, out)
	}
	// a type error in a test file is reported too
	inProject(t, map[string]string{"app.kern": mixedApp, "app_test.kern": "module app\nfunc test_a() { expect(1u64) }\n"})
	if code, out, _ := invoke(t, "", "test", "app.kern"); code != 2 || response(t, out).OK {
		t.Errorf("check error: exit %d %s", code, out)
	}
	// and with --human, the rendered diagnostics
	if code, out, _ := invoke(t, "", "test", "--human", "app.kern"); code != 2 || !strings.Contains(out, "app_test.kern:") {
		t.Errorf("human diagnostics: exit %d %s", code, out)
	}
}

// R7: an imported module's tests run, in a program without main; the
// directory's test file of another module is not loaded.
func TestKernTestLibraryOnly(t *testing.T) {
	inProject(t, map[string]string{
		"lib/lib.kern":      "module lib\nfunc Sum(a: u64, b: u64) -> u64 { return a + b }\n",
		"lib/lib_test.kern": "module lib\nfunc test_sum() { expect(Sum(2u64, 3u64) == 5u64) }\n//! expect: trap overflow\nfunc test_sum_overflows() { print_u64(Sum(18446744073709551615u64, 1u64)) }\n",
		"use/use.kern":      "module use\nimport \"lib\"\nfunc Five() -> u64 { return lib.Sum(2u64, 3u64) }\n",
		"use/use_test.kern": "module use\nfunc test_five() { expect(Five() == 5u64) }\n",
		"use/x_test.kern":   "module other\nfunc test_never() { expect(false) }\n",
	})
	code, out, _ := invoke(t, "", "test", "use/use.kern")
	_, res := testResult(t, out)
	var names []string
	for _, x := range res.Tests {
		names = append(names, x.Name+":"+x.Status)
	}
	if code != 0 || strings.Join(names, " ") != "lib.test_sum:pass lib.test_sum_overflows:pass use.test_five:pass" {
		t.Errorf("exit %d tests %v\n%s", code, names, out)
	}
}

// R8, R10: a run that does not end is a timeout in every engine.
func TestKernTestTimeout(t *testing.T) {
	inProject(t, map[string]string{
		"app.kern":      mixedApp,
		"app_test.kern": "module app\nfunc test_forever() {\n    var i: u64 = 0u64\n    while true { i = wrapping_add(i, 1u64) }\n}\n",
	})
	code, out, _ := invoke(t, "", "test", "--opt=debug", "--timeout=1", "app.kern")
	_, res := testResult(t, out)
	if code != 1 || len(res.Tests) != 1 || res.Tests[0].Status != "timeout" || res.Summary.Timeout != 1 {
		t.Fatalf("exit %d %s", code, out)
	}
	for _, r := range res.Tests[0].Runs {
		if r.Outcome != "timeout" || r.Status != -1 {
			t.Errorf("run %+v", r)
		}
	}
}

// R6, R10: an identity-trap test runs in debug only; with release alone
// it has no run and is skipped.
func TestKernTestIdentity(t *testing.T) {
	inProject(t, map[string]string{
		"app.kern": mixedApp,
		"app_test.kern": `module app
//! expect: trap identity
func test_wrong_pool() {
    p := Pool[u64].new()
    q := Pool[u64].new()
    match p.alloc(1u64) {
        Ok(a) => {
            match q.alloc(2u64) {
                Ok(b) => { print_u64(q.get(a)) }
                Err(e) => {}
            }
        }
        Err(e) => {}
    }
}
`,
	})
	code, out, _ := invoke(t, "", "test", "app.kern")
	_, res := testResult(t, out)
	if code != 0 || res.Tests[0].Status != "pass" {
		t.Fatalf("exit %d %s", code, out)
	}
	for _, r := range res.Tests[0].Runs {
		if r.Opt != "debug" {
			t.Errorf("an identity test ran in %s", r.Opt)
		}
	}
	code, out, _ = invoke(t, "", "test", "--opt=release", "app.kern")
	if _, res := testResult(t, out); code != 0 || res.Tests[0].Status != "skipped" || len(res.Tests[0].Runs) != 0 || res.Summary.Skipped != 1 {
		t.Errorf("release only: exit %d %s", code, out)
	}
	if code, human, _ := invoke(t, "", "test", "--opt=release", "--human", "app.kern"); code != 0 || !strings.HasPrefix(human, "SKIP app.test_wrong_pool\n") {
		t.Errorf("human skip: exit %d %s", code, human)
	}
}

// No test file: an empty result, ok, exit 0.
func TestKernTestNone(t *testing.T) {
	inProject(t, map[string]string{"app.kern": mixedApp})
	code, out, _ := invoke(t, "", "test", "app.kern")
	if resp, res := testResult(t, out); code != 0 || !resp.OK || len(res.Tests) != 0 {
		t.Errorf("exit %d %s", code, out)
	}
}

// R11: an internal error is one INTERNAL diagnostic and exit 3.
func TestKernTestInternal(t *testing.T) {
	inProject(t, map[string]string{"app.kern": mixedApp, "app_test.kern": "module app\nfunc test_a() {}\n"})
	saved := testHook
	t.Cleanup(func() { testHook = saved })
	testHook = func([]*source.File, testrun.Options) (*testrun.Result, []diag.Diagnostic, error) {
		return nil, nil, &testrun.InternalError{Err: errors.New("native build (debug): clang failed")}
	}
	code, out, _ := invoke(t, "", "test", "app.kern")
	if resp := response(t, out); code != 3 || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "INTERNAL" || !strings.Contains(resp.Diagnostics[0].Message, "native build") {
		t.Errorf("exit %d %s", code, out)
	}
}

// The example project's tests all pass (spec §5).
func TestKernTestExample(t *testing.T) {
	src, err := filepath.Abs(filepath.Join("..", "..", "examples", "testing"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(src)
	code, out, _ := invoke(t, "", "test")
	_, res := testResult(t, out)
	if code != 0 || len(res.Tests) < 4 || res.Summary.Pass != len(res.Tests) {
		t.Errorf("exit %d %s", code, out)
	}
}
