package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/incremental"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/source"
)

// The three programs of specs/freestanding-availability R1, each with the
// one diagnostic it gets: the quoted call and the message.
var hostedPrograms = []struct{ src, call, message string }{
	{`module m
import "std/time"
func main() -> i32 {
    t := time.wall_nanos()
    print_u64(t)
    return 0i32
}
`, "time.wall_nanos()", "call to `time.wall_nanos` needs the hosted runtime (`kern_time_wall_ns`), which a freestanding image does not have"},
	{`module m
import "std/io"
func main() -> i32 {
    print_u64(u64(io.io_argc()))
    return 0i32
}
`, "io.io_argc()", "call to `io_argc` needs the hosted runtime (`kern_io_argc`), which a freestanding image does not have"},
	{`module m
import "std/time"
func nap() { print_u64(time.wall_nanos()) }
func main() -> i32 {
    nap()
    return 0i32
}
`, "time.wall_nanos()", "call to `time.wall_nanos` needs the hosted runtime (`kern_time_wall_ns`), which a freestanding image does not have"},
}

func writeProgram(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.kern")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// onlyHosted checks that out is a failed response with exactly the one
// ENV_HOSTED_ONLY diagnostic at call.
func onlyHosted(t *testing.T, what, out, src, call, message string) {
	t.Helper()
	var resp struct {
		OK          bool              `json:"ok"`
		Diagnostics []diag.Diagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("%s: stdout is not one response (%v):\n%s", what, err, out)
	}
	if resp.OK || len(resp.Diagnostics) != 1 {
		t.Fatalf("%s: %s", what, out)
	}
	d := resp.Diagnostics[0]
	if d.Code != "ENV_HOSTED_ONLY" || d.Rule != "6.8" || d.Message != message || src[d.Span.Start:d.Span.End] != call || d.Fix != nil || len(d.Related) != 0 {
		t.Errorf("%s: %+v, want ENV_HOSTED_ONLY at %q: %s", what, d, call, message)
	}
}

// check, build and run refuse each program in freestanding mode at both
// levels, with exit code 2 and its one diagnostic; build writes nothing
// and never reaches the native build (which also covers the archive
// written without ld.lld); run never starts the interpreter (R1, F3).
func TestHostedOnlyCommands(t *testing.T) {
	builds := 0
	saved := buildHook
	buildHook = func(srcs []*source.File, out string, opts native.Options) ([]diag.Diagnostic, error) {
		builds++
		return saved(srcs, out, opts)
	}
	defer func() { buildHook = saved }()
	for _, p := range hostedPrograms {
		path := writeProgram(t, p.src)
		for _, level := range []string{"debug", "release"} {
			code, out, _ := invoke(t, "", "check", "--env=freestanding", "--opt="+level, path)
			if code != 2 {
				t.Errorf("check %s: exit %d", level, code)
			}
			onlyHosted(t, "check "+level, out, p.src, p.call, p.message)
			code, out, _ = invoke(t, "", "run", "--env=freestanding", "--opt="+level, path)
			if code != 2 {
				t.Errorf("run %s: exit %d", level, code)
			}
			onlyHosted(t, "run "+level, out, p.src, p.call, p.message) // and nothing the program printed
			img := filepath.Join(t.TempDir(), "image")
			code, out, _ = invoke(t, "", "build", "--env=freestanding", "--opt="+level, "-o", img, path)
			if code != 2 {
				t.Errorf("build %s: exit %d", level, code)
			}
			onlyHosted(t, "build "+level, out, p.src, p.call, p.message)
			if _, err := os.Stat(img); err == nil {
				t.Errorf("build %s wrote %s", level, img)
			}
		}
	}
	if builds != 0 {
		t.Errorf("the native build was reached %d times", builds)
	}
}

// In hosted mode the same programs check, run and build as before (R1).
func TestHostedOnlyHostedUnchanged(t *testing.T) {
	for i, p := range hostedPrograms {
		path := writeProgram(t, p.src)
		if code, out, _ := invoke(t, "", "check", path); code != 0 {
			t.Errorf("program %d: hosted check exit %d: %s", i, code, out)
		}
		if code, out, errOut := invoke(t, "", "run", path); code != 0 || strings.Contains(out, "ENV_HOSTED_ONLY") {
			t.Errorf("program %d: hosted run exit %d: %s%s", i, code, out, errOut)
		}
		if _, err := exec.LookPath("clang"); err == nil {
			exe := filepath.Join(t.TempDir(), "prog")
			if code, out, _ := invoke(t, "", "build", "-o", exe, path); code != 0 {
				t.Errorf("program %d: hosted build exit %d: %s", i, code, out)
			}
		}
	}
}

// A manifest's env sets freestanding mode with no flag, and a resolution
// or checking error is reported alone, never beside an ENV_HOSTED_ONLY
// (F3).
func TestHostedOnlyManifestAndErrors(t *testing.T) {
	dir := t.TempDir()
	src := hostedPrograms[0].src
	if err := os.WriteFile(filepath.Join(dir, "m.kern"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, project.ManifestName), []byte(`{"entry": "m.kern", "env": "freestanding"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := invoke(t, "", "check", filepath.Join(dir, "m.kern"))
	if code != 2 {
		t.Errorf("manifest check: exit %d", code)
	}
	onlyHosted(t, "manifest check", out, src, hostedPrograms[0].call, hostedPrograms[0].message)

	bad := writeProgram(t, `module m
import "std/time"
func main() -> i32 {
    t := time.wall_nanos()
    x: u64 = true
    return 0i32
}
`)
	code, out, _ = invoke(t, "", "check", "--env=freestanding", bad)
	if code != 2 || strings.Contains(out, "ENV_HOSTED_ONLY") || !strings.Contains(out, `"ok":false`) {
		t.Errorf("a checking error with a hosted call: exit %d\n%s", code, out)
	}
}

// Hosted check never enters the evaluation, and an evaluation failure is
// an internal error that builds and runs nothing (R6, the plan's failure
// semantics).
func TestHostedOnlyEvaluationEntry(t *testing.T) {
	calls := 0
	counting := hostedDeps{lower: Build, analyze: func(mod *ir.Module, triple string) (analysis.HostedResult, error) {
		calls++
		return analysis.HostedOnly(mod, triple)
	}}
	path := writeProgram(t, hostedPrograms[0].src)
	cwd, _ := os.Getwd()
	pr := project.LoadTests(cwd, []string{path})
	req := Request{SchemaVersion: diag.SchemaVersion, Command: "check", Files: pr.Names(), Target: native.HostTriple()}
	for _, env := range []string{"hosted", "freestanding"} {
		var out, errb strings.Builder
		cfg := incremental.Config{Opt: "debug", Env: env, Target: req.Target}
		runCheck(req, diag.SchemaVersion, pr.Files, false, "", cfg, freestandingTriple(""), &out, &errb, checkDeps{budget: budgetDefaults(), hosted: counting})
	}
	if calls != 1 {
		t.Errorf("the evaluation ran %d times for one hosted and one freestanding check, want 1", calls)
	}

	failing := hostedDeps{lower: Build, analyze: func(*ir.Module, string) (analysis.HostedResult, error) {
		return analysis.HostedResult{}, errors.New("injected")
	}}
	builds := 0
	savedHook, savedBuild, savedRun := buildHook, buildHosted, runHosted
	buildHook = func([]*source.File, string, native.Options) ([]diag.Diagnostic, error) { builds++; return nil, nil }
	buildHosted, runHosted = failing, failing
	defer func() { buildHook, buildHosted, runHosted = savedHook, savedBuild, savedRun }()
	img := filepath.Join(t.TempDir(), "image")
	if code, out, _ := invoke(t, "", "build", "--env=freestanding", "-o", img, path); code != 3 || !strings.Contains(out, "injected") {
		t.Errorf("failing evaluation in build: exit %d\n%s", code, out)
	}
	if code, out, _ := invoke(t, "", "run", "--env=freestanding", path); code != 3 || strings.Contains(out, "halt") {
		t.Errorf("failing evaluation in run: exit %d\n%s", code, out)
	}
	if builds != 0 {
		t.Errorf("the native build was reached after a failing evaluation")
	}
}
