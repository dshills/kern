package interp

import (
	"bytes"
	"errors"
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

	"github.com/dshills/kern/compiler/source"
)

// std/process in the interpreter (specs/std-process PLAN.md Phase 2): the
// wait failure path, the completion barrier of inherited streams copied
// into captured writers, inheriting into a file writer, the descriptor
// sweep, and that fixtures leave no child unwaited.

// procPrelude prints a command's status or error.
const procPrelude = `module t
import "std/io"
import "std/process"
func say(s: view[]u8) { print_str(s); println() }
func show_err(e: view[io.IoErr]) {
    match e {
        NotFound => say("not found")
        Permission => say("permission")
        Exists => say("exists")
        Other(code) => { print_str("other "); print_i64(i64(code)); println() }
    }
}
func show(r: view[Result[process.ExitStatus, io.IoErr]]) {
    match r {
        Ok(s) => {
            match s {
                Exited(code) => { print_str("exited "); print_i64(i64(code)); println() }
                Signaled(g) => { print_str("signaled "); print_i64(i64(g)); println() }
            }
        }
        Err(e) => show_err(e)
    }
}
func sh(script: view[]u8) -> process.Command {
    c := process.Command.new("sh")
    c.arg("-c")
    c.arg(script)
    return c
}
`

// runProc runs src in the interpreter with the given writers and returns
// its exit status.
func runProc(t *testing.T, src string, out, errOut io.Writer) int {
	t.Helper()
	mod, msg := lowerFor([]*source.File{{Name: "t.kern", Bytes: []byte(src)}}, "debug")
	if mod == nil {
		t.Errorf("lower: %s", msg) // not Fatalf: runProc also runs on watchdog goroutines
		return -1
	}
	return New(mod, "t.kern", out, errOut).Run()
}

// syncBuffer is a captured writer the test may read while a forwarder
// still writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// withHook sets procHook for the test.
func withHook(t *testing.T, h func(string)) {
	procHook.Store(&h)
	t.Cleanup(func() { procHook.Store(nil) })
}

// watchdog turns a hang into a failure. A hung scenario cannot be
// stopped from here, and leaving it running would let it reach later
// tests (procHook, the test's writers), so a timeout ends the whole test
// binary, with every goroutine's stack.
func watchdog(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		panic(fmt.Sprintf("%s: %s did not finish within 10s", t.Name(), what))
	}
}

// TestProcWaitFailure: when the system refuses a wait (here the child is
// reaped behind the Child's back, at the first wait, so the system says
// ECHILD), try_wait returns the error and leaves the Child unchanged (a
// second try_wait asks the system again rather than reporting a stored
// status), and wait returns the error too (R5).
func TestProcWaitFailure(t *testing.T) {
	var once sync.Once
	withHook(t, func(s string) {
		if s == "wait" {
			once.Do(func() {
				var ws syscall.WaitStatus
				_, _ = syscall.Wait4(-1, &ws, 0, nil) // the test's only child
			})
		}
	})
	src := procPrelude + `func scenario() -> Result[(), io.IoErr] {
    cmd := process.Command.new("true")
    c := cmd.spawn()?
    t := c.try_wait()
    match t {
        Ok(o) => say("try_wait ok")
        Err(e) => show_err(e)
    }
    t2 := c.try_wait()
    match t2 {
        Ok(o) => say("try_wait ok")
        Err(e) => show_err(e)
    }
    w := c.wait()
    show(w)
    return Ok(())
}
func main() -> i32 {
    r := scenario()
    return 0i32
}
`
	var out bytes.Buffer
	if status := runProc(t, src, &out, os.Stderr); status != 0 {
		t.Fatalf("status %d", status)
	}
	echild := fmt.Sprintf("other %d\n", int(syscall.ECHILD))
	want := echild + echild + echild
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
}

// TestProcForwarding: an inherited stream copied into a captured writer.
// wait returns once everything the child wrote is in the writer, and a
// background grandchild that still holds the stream cannot extend it.
func TestProcForwarding(t *testing.T) {
	tmp := t.TempDir()
	hold, done := filepath.Join(tmp, "hold"), filepath.Join(tmp, "done")
	for _, p := range []string{hold, done} {
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src := procPrelude + `func main() -> i32 {
    c := sh("(cat \"$KSP_HOLD\" >/dev/null; printf late; echo gone >\"$KSP_DONE\") & printf early")
    c.env_set("KSP_HOLD", "` + hold + `")
    c.env_set("KSP_DONE", "` + done + `")
    r := c.status()
    show(r)
    return 0i32
}
`
	var out syncBuffer
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		watchdog(t, "releasing the grandchild", func() {
			if f, err := os.OpenFile(hold, os.O_WRONLY, 0); err == nil {
				_, _ = f.WriteString("go\n")
				_ = f.Close()
			}
			if f, err := os.Open(done); err == nil {
				_, _ = io.ReadAll(f)
				_ = f.Close()
			}
		})
	}
	t.Cleanup(release)
	watchdog(t, "the program", func() {
		if status := runProc(t, src, &out, os.Stderr); status != 0 {
			t.Errorf("status %d", status)
		}
	})
	if got := out.String(); got != "earlyexited 0\n" {
		t.Fatalf("before the grandchild ends: %q", got)
	}
	release()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "late") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "late") {
		t.Fatalf("the grandchild's output never reached the writer: %q", out.String())
	}
}

// slowWriter sleeps before every write.
type slowWriter struct {
	syncBuffer
	d time.Duration
}

func (s *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(s.d)
	return s.syncBuffer.Write(p)
}

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, errors.New("refused") }

// TestProcForwardingWriters: a slow writer still receives everything
// before wait returns; a failing one does not block the child, which
// still ends normally (R4); try_wait on a child that is blocked writing
// returns None at once.
func TestProcForwardingWriters(t *testing.T) {
	big := procPrelude + `func main() -> i32 {
    c := process.Command.new("head")
    c.arg("-c")
    c.arg("1048576")
    c.arg("/dev/zero")
    r := c.status()
    match r {
        Ok(s) => { if s.success() { return 0i32 } }
        Err(e) => { }
    }
    return 1i32
}
`
	slow := &slowWriter{d: 10 * time.Millisecond}
	watchdog(t, "a slow writer", func() {
		if status := runProc(t, big, slow, os.Stderr); status != 0 {
			t.Errorf("slow writer: status %d", status)
		}
	})
	if n := len(slow.String()); n != 1<<20 {
		t.Fatalf("slow writer received %d bytes, want %d", n, 1<<20)
	}
	watchdog(t, "a failing writer", func() {
		if status := runProc(t, big, failWriter{}, os.Stderr); status != 0 {
			t.Errorf("failing writer: status %d", status)
		}
	})
	poll := procPrelude + `func scenario() -> Result[(), io.IoErr] {
    cmd := process.Command.new("head")
    cmd.arg("-c")
    cmd.arg("67108864")
    cmd.arg("/dev/zero")
    c := cmd.spawn()?
    t := c.try_wait()
    match t {
        Ok(o) => {
            match o {
                Some(s) => eprint_str("ended")
                None => eprint_str("running")
            }
        }
        Err(e) => eprint_str("error")
    }
    k := c.kill()
    w := c.wait()
    match w {
        Ok(s) => {
            match s {
                Signaled(g) => { if g == 9i32 { eprint_str(" killed") } }
                Exited(code) => eprint_str(" exited")
            }
        }
        Err(e) => eprint_str(" wait failed")
    }
    return Ok(())
}
func main() -> i32 {
    r := scenario()
    return 0i32
}
`
	var errb syncBuffer
	slower := &slowWriter{d: 50 * time.Millisecond}
	watchdog(t, "try_wait on a writing child", func() {
		start := time.Now()
		if status := runProc(t, poll, slower, &errb); status != 0 {
			t.Errorf("status %d", status)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("took %v", d)
		}
	})
	if got := errb.String(); got != "running killed" {
		t.Fatalf("got %q", got)
	}
}

// TestProcForwardingStates: the forwarder's state machine, sequenced with
// procHook rather than timing. A flush request that arrives after the
// forwarder checked for one and before its read (the read must then time
// out without consuming the child's bytes, which the drain then copies);
// a forwarder already done before the request; and the barrier running
// once when try_wait reaped the child before wait.
func TestProcForwardingStates(t *testing.T) {
	src := procPrelude + `func main() -> i32 {
    c := process.Command.new("printf")
    c.arg("abc")
    r := c.status()
    show(r)
    return 0i32
}
`
	t.Run("request between check and read", func(t *testing.T) {
		requested := make(chan struct{})
		var once sync.Once
		var mu sync.Mutex
		var states []string
		held := false
		withHook(t, func(s string) {
			mu.Lock()
			states = append(states, s)
			first := s == "read" && !held // the forwarder's first read, right after its first check
			if first {
				held = true
			}
			mu.Unlock()
			switch {
			case first:
				<-requested
			case s == "requested":
				once.Do(func() { close(requested) })
			}
		})
		var out syncBuffer
		watchdog(t, "the program", func() { runProc(t, src, &out, os.Stderr) })
		if out.String() != "abcexited 0\n" {
			t.Fatalf("got %q", out.String())
		}
		mu.Lock()
		defer mu.Unlock()
		joined := strings.Join(states, " ")
		if !strings.Contains(joined, "flushing") {
			t.Fatalf("the drain did not run: %s", joined)
		}
	})
	t.Run("done before the request", func(t *testing.T) {
		doneCh := make(chan struct{})
		var once sync.Once
		withHook(t, func(s string) {
			switch s {
			case "done":
				once.Do(func() { close(doneCh) })
			case "request":
				<-doneCh
			}
		})
		var out syncBuffer
		watchdog(t, "the program", func() { runProc(t, src, &out, os.Stderr) })
		if out.String() != "abcexited 0\n" {
			t.Fatalf("got %q", out.String())
		}
	})
	t.Run("barrier once", func(t *testing.T) {
		var mu sync.Mutex
		requests := 0
		withHook(t, func(s string) {
			if s == "request" {
				mu.Lock()
				requests++
				mu.Unlock()
			}
		})
		twice := procPrelude + `func scenario() -> Result[(), io.IoErr] {
    cmd := process.Command.new("printf")
    cmd.arg("abc")
    c := cmd.spawn()?
    var ended: bool = false
    while !ended {
        t := c.try_wait()?
        match t {
            Some(s) => ended = true
            None => { }
        }
    }
    w := c.wait()
    show(w)
    return Ok(())
}
func main() -> i32 {
    r := scenario()
    return 0i32
}
`
		var out syncBuffer
		watchdog(t, "the program", func() { runProc(t, twice, &out, os.Stderr) })
		if out.String() != "abcexited 0\n" {
			t.Fatalf("got %q", out.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if requests != 1 {
			t.Fatalf("%d flush requests, want 1", requests)
		}
	})
}

// TestProcInheritFile: when the program's standard output is a file, a
// child inherits that file's own descriptor (above 2 here, and marked
// close-on-exec, as Go's files are), not the interpreter's descriptor 1.
func TestProcInheritFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if f.Fd() < 3 {
		t.Fatalf("descriptor %d", f.Fd())
	}
	syscall.CloseOnExec(int(f.Fd()))
	src := procPrelude + `func main() -> i32 {
    c := process.Command.new("printf")
    c.arg("from the child\n")
    r := c.status()
    show(r)
    return 0i32
}
`
	if status := runProc(t, src, f, os.Stderr); status != 0 {
		t.Fatalf("status %d", status)
	}
	got, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from the child\nexited 0\n" {
		t.Fatalf("got %q", got)
	}
}

// TestProcInheritPollablePipe: when the program's standard output is a
// non-blocking file (a pipe Go polls), the child's output is copied
// through a pipe of its own rather than handed the host's descriptor,
// which would give the child a non-blocking stdout, and the host's file
// keeps its mode: its write deadlines still work.
func TestProcInheritPollablePipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }() // a second Close after the one below only errors
	got := make(chan string, 1)      // the reader can finish even when the test fails early
	go func() { b, _ := io.ReadAll(r); got <- string(b) }()
	src := procPrelude + `func main() -> i32 {
    c := process.Command.new("printf")
    c.arg("from the child\n")
    r := c.status()
    show(r)
    return 0i32
}
`
	if status := runProc(t, src, w, os.Stderr); status != 0 {
		t.Fatalf("status %d", status)
	}
	if err := w.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("the host's pipe lost its mode: %v", err)
	}
	_ = w.Close()
	if s := <-got; s != "from the child\nexited 0\n" {
		t.Fatalf("got %q", s)
	}
}

// TestProcSweep: a descriptor the host process opened without
// close-on-exec does not reach a child: the spawn marks it first.
func TestProcSweep(t *testing.T) {
	// the baseline first: os/exec closes nothing, so an ls it starts
	// after the open would list the descriptor too
	baseline, err := exec.Command("ls", "/dev/fd").Output()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(os.DevNull, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	src := procPrelude + `func main() -> i32 {
    c := process.Command.new("ls")
    c.arg("/dev/fd")
    r := c.status()
    return 0i32
}
`
	var out syncBuffer
	if status := runProc(t, src, &out, os.Stderr); status != 0 {
		t.Fatalf("status %d", status)
	}
	if out.String() != string(baseline) {
		t.Fatalf("descriptor %d open: child lists\n%s\nwant\n%s", fd, out.String(), baseline)
	}
}

// TestProcNoZombies: the std/process fixtures wait for every child they
// start. They run here in the test process, so their children are this
// process's own; none may be left unreaped.
func TestProcNoZombies(t *testing.T) {
	names := []string{"std_process_run", "std_process_env", "std_process_raw", "std_process_pipes",
		"std_process_output", "std_process_output_faults", "std_process_threads"}
	for _, name := range names {
		src, err := os.ReadFile(filepath.Join("..", "..", "tests", "run", name+".kern"))
		if err != nil {
			t.Fatal(err)
		}
		mod, msg := lowerFor([]*source.File{{Name: name + ".kern", Bytes: src}}, "debug")
		if mod == nil {
			t.Fatalf("%s: %s", name, msg)
		}
		var out syncBuffer
		it := New(mod, name+".kern", &out, os.Stderr)
		it.TestHooks = bytes.Contains(src, []byte("//! test-hooks: true"))
		if status := it.Run(); status != 0 {
			t.Fatalf("%s: status %d", name, status)
		}
	}
	var ws syscall.WaitStatus
	if pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("an unwaited child remains: pid %d, err %v", pid, err)
	}
}
