package tests

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/source"
)

// TestProcessFreestanding: std/process is hosted only, like std/io
// (specs/std-process §1 constraint 5). A freestanding build that calls it
// fails in the same way as one calling std/io: the image links against no
// runtime entry of either module, so the link reports the missing kern_*
// symbols. No new diagnostic is added for it.
func TestProcessFreestanding(t *testing.T) {
	if !native.Available() || !native.Linkable() {
		t.Skip("clang or ld.lld not available")
	}
	progs := map[string]string{
		"kern_io_": `module t
import "std/io"
func main() -> u64 {
    with r := io.read_file("x") {
        match r {
            Ok(v) => return 1u64
            Err(e) => return 2u64
        }
    }
    return 0u64
}
`,
		"kern_proc_": `module t
import "std/process"
func main() -> u64 {
    with r := process.env("PATH") {
        match r {
            Some(v) => return 1u64
            None => return 2u64
        }
    }
    return 0u64
}
`,
	}
	for prefix, src := range progs {
		files := []*source.File{{Name: "t.kern", Bytes: []byte(src)}}
		img := filepath.Join(t.TempDir(), "image")
		ds, err := native.Build(files, img, native.Options{Freestanding: true, Triple: native.FreestandingTriple})
		if len(ds) > 0 {
			t.Fatalf("%s: the program should check cleanly, got %s", prefix, render(ds))
		}
		if err == nil {
			t.Fatalf("%s: a freestanding build calling a hosted module linked", prefix)
		}
		if !strings.Contains(err.Error(), "undefined symbol: "+prefix) {
			t.Fatalf("%s: want an undefined %s* symbol, got %v", prefix, prefix, err)
		}
	}
}

// The TestProcess* tests below run a small kern program as the process
// under test, so that each controls that process's working directory,
// descriptors, environment and signals: once in the interpreter (`kern
// run`) and once natively (the binary `kern build` makes), and require
// the expected output from both (specs/std-process PLAN.md Phase 2,
// step 8).

// TestMain removes the kern binary the TestProcess* tests share.
func TestMain(m *testing.M) {
	code := m.Run()
	if kernBin != "" {
		_ = os.RemoveAll(filepath.Dir(kernBin))
	}
	os.Exit(code)
}

var (
	kernBinOnce sync.Once
	kernBin     string
	kernBinErr  error
)

// kernBinary builds cmd/kern once for the package's tests.
func kernBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the kern binary")
	}
	if !native.Available() {
		t.Skip("clang not available")
	}
	kernBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kern-process-test-")
		if err != nil {
			kernBinErr = err
			return
		}
		kernBin = filepath.Join(dir, "kern") // removed by TestMain, even when the build fails
		if out, err := exec.Command("go", "build", "-o", kernBin, "../cmd/kern").CombinedOutput(); err != nil {
			kernBinErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if kernBinErr != nil {
		t.Fatal(kernBinErr)
	}
	return kernBin
}

// processEngine is one way to run the program: its argument vector.
type processEngine struct {
	name string
	argv []string
}

// processEngines writes src as a program and returns how to run it in the
// interpreter and natively.
func processEngines(t *testing.T, src string) []processEngine {
	t.Helper()
	kern := kernBinary(t)
	dir := t.TempDir()
	prog := filepath.Join(dir, "prog.kern")
	if err := os.WriteFile(prog, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "prog")
	if out, err := exec.Command(kern, "build", "-o", bin, prog).CombinedOutput(); err != nil {
		t.Fatalf("kern build: %v\n%s", err, out)
	}
	return []processEngine{{"interp", []string{kern, "run", prog}}, {"native", []string{bin}}}
}

// launch starts argv with syscall.ForkExec, so that the caller chooses its
// descriptors exactly (files[i] for descriptor i, ^uintptr(0) closed;
// descriptor 1 is always captured) and its environment as is (duplicates
// kept), and returns its standard output and exit status. It runs in a
// process group of its own, killed with every descendant when it has not
// finished within a minute, so that a hang fails the test that caused it.
const launchLimit = time.Minute

func launch(t *testing.T, argv []string, dir string, env []string, files []uintptr) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if files == nil {
		files = []uintptr{0, 1, 2}
	}
	files = append([]uintptr(nil), files...)
	files[1] = w.Fd()
	if env == nil {
		env = os.Environ()
	}
	attr := &syscall.ProcAttr{Dir: dir, Env: env, Files: files, Sys: &syscall.SysProcAttr{Setpgid: true}}
	pid, err := syscall.ForkExec(argv[0], argv, attr)
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		t.Fatalf("start %v: %v", argv, err)
	}
	expired := make(chan struct{})
	timer := time.AfterFunc(launchLimit, func() {
		close(expired)
		_ = syscall.Kill(-pid, syscall.SIGKILL) // the group: the program and every child it left
	})
	out, _ := io.ReadAll(r)
	_ = r.Close()
	var ws syscall.WaitStatus
	for {
		_, err = syscall.Wait4(pid, &ws, 0, nil)
		if err != syscall.EINTR {
			break
		}
	}
	timer.Stop()
	if err != nil {
		t.Fatalf("wait %v: %v", argv, err)
	}
	select {
	case <-expired:
		t.Fatalf("%v did not finish within %v; output so far:\n%s", argv, launchLimit, out)
	default:
	}
	return string(out), ws.ExitStatus()
}

// processPrelude is shared by the programs: running a command and
// printing its status or error.
const processPrelude = `module t
import "std/io"
import "std/process"
func say(s: view[]u8) { print_str(s); println() }
func run(c: view[process.Command]) {
    r := c.status()
    match r {
        Ok(s) => {
            match s {
                Exited(code) => { print_str("exited "); print_i64(i64(code)); println() }
                Signaled(g) => { print_str("signaled "); print_i64(i64(g)); println() }
            }
        }
        Err(e) => {
            match e {
                NotFound => say("not found")
                Permission => say("permission")
                Exists => say("exists")
                Other(code) => { print_str("other "); print_i64(i64(code)); println() }
            }
        }
    }
}
func sh(script: view[]u8) -> process.Command {
    c := process.Command.new("sh")
    c.arg("-c")
    c.arg(script)
    return c
}
`

// expectEngines runs each engine with the same launch settings and
// compares its output with want(engine).
func expectEngines(t *testing.T, src string, dir string, env []string, files []uintptr, want func(engine string) string, wrap ...string) {
	t.Helper()
	for _, e := range processEngines(t, src) {
		argv := append(append([]string(nil), wrap...), e.argv...)
		out, status := launch(t, argv, dir, env, files)
		if status != 0 || out != want(e.name) {
			t.Errorf("%s: status %d, output\n%s\nwant\n%s", e.name, status, out, want(e.name))
		}
	}
}

// TestProcessSpawnOrder: R4's error order when several failures apply. A
// dir that is a file beats a missing program (ENOTDIR, step (a) before
// (b)); a missing program is ENOENT even when a non-executable file of
// that name comes first on the search list; a non-executable ./x is
// EACCES (step (c)).
func TestProcessSpawnOrder(t *testing.T) {
	tmp := t.TempDir()
	for name, mode := range map[string]os.FileMode{"file": 0o644, "noexec/kspprog": 0o644, "x": 0o644} {
		p := filepath.Join(tmp, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	src := processPrelude + `func main() -> i32 {
    a := process.Command.new("kern-no-such-program")
    a.dir("` + filepath.Join(tmp, "file") + `")
    run(a)
    b := process.Command.new("kspprog")
    run(b)
    c := process.Command.new("./x")
    c.dir("` + tmp + `")
    run(c)
    return 0i32
}
`
	env := []string{"PATH=" + filepath.Join(tmp, "noexec") + ":/usr/bin:/bin"}
	want := fmt.Sprintf("other %d\nnot found\npermission\n", int(syscall.ENOTDIR))
	expectEngines(t, src, "", env, nil, func(string) string { return want })
}

// TestProcessRelativeDir: a relative dir and a relative program holding a
// '/' are each resolved once, against the program's working directory and
// the child's (a doubled a/a/b/tool would be ENOENT).
func TestProcessRelativeDir(t *testing.T) {
	tmp := t.TempDir()
	tool := filepath.Join(tmp, "a", "b", "tool")
	if err := os.MkdirAll(filepath.Dir(tool), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho tool ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := processPrelude + `func main() -> i32 {
    a := process.Command.new("b/tool")
    a.dir("a")
    run(a)
    b := process.Command.new("./b/tool")
    b.dir("a")
    run(b)
    c := process.Command.new("a/b/tool")
    run(c)
    return 0i32
}
`
	want := "tool ran\nexited 0\ntool ran\nexited 0\ntool ran\nexited 0\n"
	expectEngines(t, src, tmp, nil, nil, func(string) string { return want })
}

// TestProcessDescriptors: a child inherits exactly descriptors 0 to 2
// (R4). With an extra descriptor 3 that has no close-on-exec flag, `ls
// /dev/fd` in the child lists what it lists when started with 0 to 2
// only. With descriptor 0
// closed, an inherited stdin is closed in the child natively (the child
// asks with test -e /dev/fd/0, which opens nothing: a redirection would
// open a file onto the free descriptor 0 and see it open); in the
// interpreter, Go's runtime has already reopened a closed descriptor 0 on
// /dev/null at start (Go 1.22 and later), so the program's stdin, and so
// the child's, is open.
func TestProcessDescriptors(t *testing.T) {
	extra, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = extra.Close() }()
	ls := processPrelude + `func main() -> i32 {
    c := process.Command.new("ls")
    c.arg("/dev/fd")
    run(c)
    return 0i32
}
`
	// what ls itself opens differs by system (macOS's opens two
	// descriptors), so the child's listing is compared with that of an ls
	// started with descriptors 0 to 2 only
	baseline, _ := launch(t, []string{"/bin/ls", "/dev/fd"}, "", nil, nil)
	// syscall.ForkExec clears close-on-exec on the descriptors it places
	expectEngines(t, ls, "", nil, []uintptr{0, 1, 2, extra.Fd()}, func(string) string { return baseline + "exited 0\n" })

	probe := processPrelude + `func main() -> i32 {
    c := sh("if [ -e /dev/fd/0 ]; then echo open; else echo closed; fi")
    run(c)
    return 0i32
}
`
	stdinClosed := func(e string) string {
		if e == "interp" {
			return "open\nexited 0\n"
		}
		return "closed\nexited 0\n"
	}
	expectEngines(t, probe, "", nil, []uintptr{^uintptr(0), 1, 2}, stdinClosed)

	// the same with the child's stdout piped: neither the pipe nor anything
	// else takes the place of the closed descriptor 0, in the child or in
	// the program (PLAN.md Phase 3)
	pipedProbe := processPrelude + `func scenario() -> Result[(), io.IoErr] {
    c := sh("if [ -e /dev/fd/0 ]; then echo open; else echo closed; fi")
    c.stdout(process.Stdio.Piped)
    ch := c.spawn()?
    r := ch.take_stdout()?
    data := r.read_all()?
    with b := data.as_view() { print_str(b) }
    st := ch.wait()
    match st {
        Ok(s) => {
            match s {
                Exited(code) => { print_str("exited "); print_i64(i64(code)); println() }
                Signaled(g) => say("signaled")
            }
        }
        Err(e) => say("wait failed")
    }
    return Ok(())
}
func main() -> i32 {
    r := scenario()
    match r {
        Ok(u) => { }
        Err(e) => say("failed")
    }
    return 0i32
}
`
	expectEngines(t, pipedProbe, "", nil, []uintptr{^uintptr(0), 1, 2}, stdinClosed)
}

// TestProcessEnvParity: both engines see the environment the program
// started with in its order, keeping the first entry of a repeated name,
// and pass it on unchanged when no edit is recorded.
func TestProcessEnvParity(t *testing.T) {
	path := "PATH=" + os.Getenv("PATH")
	env := []string{path, "A=1", "B=2", "A=3", "C="}
	src := processPrelude + `func main() -> i32 {
    with r := process.env("A") {
        match r {
            Some(v) => { print_str("A is "); with b := v.as_view() { print_str(b) }; println() }
            None => say("A unset")
        }
    }
    c := process.Command.new("/usr/bin/env")
    run(c)
    return 0i32
}
`
	want := "A is 1\n" + path + "\nA=1\nB=2\nC=\nexited 0\n"
	expectEngines(t, src, "", env, nil, func(string) string { return want })
}

// TestProcessSignals: signals ignored when the program started stay
// ignored in a child (SIGHUP), every other one has its default
// disposition, and SIGCHLD ignored at start does not make children
// unwaitable (R4 as amended). The program is started through sh, whose
// trap "" ignores a signal across exec.
func TestProcessSignals(t *testing.T) {
	src := processPrelude + `func main() -> i32 {
    c := sh("kill -HUP $$; echo alive")
    run(c)
    t := process.Command.new("true")
    run(t)
    return 0i32
}
`
	ignoring := func(sig string) []string { return []string{"/bin/sh", "-c", `trap "" ` + sig + `; exec "$0" "$@"`} }
	expectEngines(t, src, "", nil, nil, func(string) string { return "alive\nexited 0\nexited 0\n" }, ignoring("HUP")...)
	expectEngines(t, src, "", nil, nil, func(string) string { return "signaled 1\nexited 0\n" })
	expectEngines(t, src, "", nil, nil, func(string) string { return "signaled 1\nexited 0\n" }, ignoring("CHLD")...)
}
