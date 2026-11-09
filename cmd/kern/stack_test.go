package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const stackProgram = "module t\nfunc main() -> i32 {\n    print_u64(u64(thread.stack_bytes()))\n    println()\n    return 0i32\n}\n"

// The stack size's sources in order (specs/runtime RC2): the environment
// over the flag over the manifest over the default, the same for `kern
// run` (the interpreter's simulated stack) and for a program `kern build`
// made; an unaccepted flag or manifest value builds nothing.
func TestStackBytesSources(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.kern"), []byte(stackProgram), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	manifest := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "kern.mod"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	interp := func(args ...string) string {
		t.Helper()
		code, out, errb := invoke(t, "", append([]string{"run"}, args...)...)
		if code != 0 {
			t.Fatalf("run %v: exit %d\n%s%s", args, code, out, errb)
		}
		return strings.TrimSpace(out)
	}
	_, clangErr := exec.LookPath("clang")
	native := func(env string, args ...string) string {
		t.Helper()
		if clangErr != nil {
			return ""
		}
		exe := filepath.Join(dir, "app")
		if code, out, errb := invoke(t, "", append([]string{"build", "-o", exe}, args...)...); code != 0 {
			t.Fatalf("build %v: exit %d\n%s%s", args, code, out, errb)
		}
		cmd := exec.Command(exe)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		if env != "" {
			cmd.Env = append(cmd.Env, env)
		}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("built program: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	check := func(what, got, want string) {
		t.Helper()
		if got == "" && clangErr != nil {
			return // no clang: the native half is not run
		}
		if got != want {
			t.Errorf("%s: stack_bytes %s, want %s", what, got, want)
		}
	}
	manifest(`{"entry": "app.kern"}`)
	check("run, default", interp(), "8388608")
	check("build, default", native(""), "8388608")
	check("run, flag", interp("--stack-bytes=1048576"), "1048576")
	check("build, flag", native("", "--stack-bytes=1048576"), "1048576")
	manifest(`{"entry": "app.kern", "stack_bytes": 2097152}`)
	check("run, manifest", interp(), "2097152")
	check("build, manifest", native(""), "2097152")
	check("run, flag over manifest", interp("--stack-bytes=4194304"), "4194304")
	check("build, flag over manifest", native("", "--stack-bytes=4194304"), "4194304")
	check("build, environment over flag and manifest", native("KERN_STACK_BYTES=524288", "--stack-bytes=4194304"), "524288")
	t.Setenv("KERN_STACK_BYTES", "524288")
	check("run, environment over flag and manifest", interp("--stack-bytes=4194304"), "524288")
	t.Setenv("KERN_STACK_BYTES", "nonsense")
	check("run, an unaccepted environment value is ignored", interp(), "2097152")
	t.Setenv("KERN_STACK_BYTES", "")

	for _, bad := range []string{"--stack-bytes=65535", "--stack-bytes=1073741825", "--stack-bytes=0", "--stack-bytes=-1", "--stack-bytes=1e6", "--stack-bytes=", "--stack-bytes=+65536"} {
		for _, cmd := range []string{"run", "build"} {
			args := []string{cmd, bad}
			if cmd == "build" {
				args = append(args, "-o", filepath.Join(dir, "never"))
			}
			code, out, _ := invoke(t, "", args...)
			if code != 2 || firstCode(t, out) != "CLI_INVALID_FLAGS" {
				t.Errorf("%s %s: exit %d %s, want CLI_INVALID_FLAGS", cmd, bad, code, out)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "never")); err == nil {
		t.Errorf("a build with an unaccepted --stack-bytes wrote its output")
	}
	manifest(`{"entry": "app.kern", "stack_bytes": 100}`)
	if code, out, _ := invoke(t, "", "run"); code != 2 || firstCode(t, out) != "PROJECT_INVALID" {
		t.Errorf("an unaccepted manifest stack_bytes: exit %d %s, want PROJECT_INVALID", code, out)
	}
}

// kern run --backtrace makes the interpreter follow a trap's record with a
// backtrace line; KERN_BACKTRACE decides it either way (specs/runtime RE2).
func TestRunBacktraceFlag(t *testing.T) {
	dir := t.TempDir()
	src := "module t\nfunc inner(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nfunc main() -> i32 {\n    var xs: [2]u64 = [1u64, 2u64]\n    print_u64(inner(xs, 9usize))\n    return 0i32\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "app.kern"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	lines := func(args ...string) []string {
		t.Helper()
		code, _, errb := invoke(t, "", append([]string{"run"}, args...)...)
		if code != 134 {
			t.Fatalf("run %v: exit %d, stderr %q", args, code, errb)
		}
		return strings.Split(strings.TrimRight(errb, "\n"), "\n")
	}
	at := strings.Index(src, "xs[i]")
	col := at - strings.Index(src, "func inner") + 1
	record := fmt.Sprintf(`{"trap":{"kind":"bounds","file":"app.kern","span":{"start":%d,"end":%d},"line":2,"col":%d,"thread":0}}`, at, at+len("xs[i]"), col)
	const backtrace = `{"backtrace":{"thread":0,"frames":[{"function":"t.inner","file":"app.kern","line":2},{"function":"t.main","file":"app.kern","line":3}]}}`
	if got := lines("app.kern"); len(got) != 1 || got[0] != record {
		t.Errorf("without the flag: %q", got)
	}
	if got := lines("--backtrace", "app.kern"); len(got) != 2 || got[0] != record || got[1] != backtrace {
		t.Errorf("with the flag: %q", got)
	}
	t.Setenv("KERN_BACKTRACE", "0")
	if got := lines("--backtrace", "app.kern"); len(got) != 1 {
		t.Errorf("KERN_BACKTRACE=0 with the flag: %q", got)
	}
	t.Setenv("KERN_BACKTRACE", "1")
	if got := lines("app.kern"); len(got) != 2 || got[1] != backtrace {
		t.Errorf("KERN_BACKTRACE=1 without the flag: %q", got)
	}
}
