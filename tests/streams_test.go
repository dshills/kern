package tests

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/source"
)

// The standard streams, the runtime's configuration and exit, as a process
// around the program sees them (specs/runtime area B). A fixture cannot
// show these: they need a pipe, a terminal, a signal or an environment
// variable.

// buildProgram builds src natively and returns the executable.
func buildProgram(t *testing.T, src string, opts native.Options) string {
	t.Helper()
	if !native.Available() {
		t.Skip("clang not available")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	f := &source.File{Name: "t.kern", Bytes: []byte(src)}
	ds, err := native.Build([]*source.File{f}, exe, opts)
	if err != nil {
		t.Fatalf("native build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("diagnostics: %s", render(ds))
	}
	return exe
}

// collector reads a stream in the background, so that a test can ask what
// has arrived by a deadline and later for everything up to the end.
type collector struct {
	ch   chan []byte
	got  []byte
	done bool
}

func collect(r io.Reader) *collector {
	c := &collector{ch: make(chan []byte, 64)}
	go func() {
		for {
			buf := make([]byte, 65536)
			n, err := r.Read(buf)
			if n > 0 {
				c.ch <- buf[:n]
			}
			if err != nil {
				close(c.ch)
				return
			}
		}
	}()
	return c
}

// within waits until want bytes have arrived in all, the stream has ended
// or the time has passed, and returns everything that has arrived.
func (c *collector) within(want int, d time.Duration) []byte {
	deadline := time.After(d)
	for len(c.got) < want && !c.done {
		select {
		case b, ok := <-c.ch:
			if !ok {
				c.done = true
				break
			}
			c.got = append(c.got, b...)
		case <-deadline:
			return c.got
		}
	}
	return c.got
}

// all waits for the stream to end and returns everything.
func (c *collector) all() []byte {
	for !c.done {
		b, ok := <-c.ch
		if !ok {
			c.done = true
			break
		}
		c.got = append(c.got, b...)
	}
	return c.got
}

const promptProgram = `module t
import "std/io"
func count() -> Result[usize, io.IoErr] {
    data := io.read_stdin()?
    return Ok(data.len())
}
func main() -> i32 {
    print_str("prompt> ")
    r := count()
    match r {
        Ok(n) => { print_str("got "); print_u64(u64(n)); println() }
        Err(e) => { print_str("failed"); println() }
    }
    return 0i32
}
`

// RB2: what the program wrote is flushed before it reads standard input,
// so a prompt with no newline reaches a pipe before any input is sent.
func TestStdoutFlushedBeforeStdinRead(t *testing.T) {
	exe := buildProgram(t, promptProgram, native.Options{})
	cmd := exec.Command(exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	out := collect(stdout)
	got := out.within(len("prompt> "), 10*time.Second)
	if string(got) != "prompt> " {
		_ = cmd.Process.Kill()
		t.Fatalf("the prompt did not arrive before input was sent: %q", got)
	}
	_, _ = stdin.Write([]byte("abc"))
	_ = stdin.Close()
	all := out.all()
	if err := cmd.Wait(); err != nil || string(all) != "prompt> got 3\n" {
		t.Fatalf("after input: %v %q", err, all)
	}
}

const linesProgram = `module t
import "std/time"
func main() -> i32 {
    print_str("ready")
    println()
    time.sleep(time.millis(1500u64))
    print_str("done")
    println()
    return 0i32
}
`

// RB2: a line reaches a terminal when it is written; a pipe gets it when
// the buffer is flushed, here at exit.
func TestStdoutLineBufferedOnTerminal(t *testing.T) {
	exe := buildProgram(t, linesProgram, native.Options{})
	t.Run("terminal", func(t *testing.T) {
		master, slave, err := openPTY()
		if err != nil {
			t.Skipf("no pseudo-terminal: %v", err)
		}
		defer func() { _ = master.Close() }()
		cmd := exec.Command(exe)
		cmd.Stdout = slave
		start := time.Now()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = slave.Close()
		got := collect(master).within(len("ready\r\n"), 1200*time.Millisecond)
		early := time.Since(start)
		if !strings.Contains(string(got), "ready") || strings.Contains(string(got), "done") {
			t.Fatalf("after %v the terminal had %q, want the first line only", early, got)
		}
		_ = cmd.Wait()
	})
	t.Run("pipe", func(t *testing.T) {
		cmd := exec.Command(exe)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		out := collect(stdout)
		if got := out.within(1, 700*time.Millisecond); len(got) != 0 {
			t.Fatalf("a pipe received %q before the buffer was flushed", got)
		}
		all := out.all()
		if err := cmd.Wait(); err != nil || string(all) != "ready\ndone\n" {
			t.Fatalf("at exit: %v %q", err, all)
		}
	})
}

const floodProgram = `module t
import "std/io"
import "std/process"
func report(label: view[]u8) {
    r := io.flush_stdout()
    eprint_str(label)
    match r {
        Ok(u) => eprint_str(" ok\n")
        Err(e) => {
            match e {
                Other(c) => { if c == process.proc_errno(3u8) { eprint_str(" broken pipe\n") } else { eprint_str(" other\n") } }
                NotFound => eprint_str(" not found\n")
                Permission => eprint_str(" permission\n")
                Exists => eprint_str(" exists\n")
            }
        }
    }
}
func main() -> i32 {
    i := 0u64
    while i < 20000u64 {
        print_str("0123456789012345678901234567890123456789012345678")
        println()
        i = i + 1u64
    }
    report("first")
    report("second")
    return 0i32
}
`

// RB3: the runtime leaves SIGPIPE alone. With the default disposition a
// write to a pipe nobody reads ends the process by that signal; with the
// signal ignored the write is a recorded failure that flush_stdout returns
// once.
func TestStdoutBrokenPipe(t *testing.T) {
	exe := buildProgram(t, floodProgram, native.Options{})
	run := func(argv ...string) (stderr string, ws syscall.WaitStatus) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Close() // nobody reads
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout = w
		var eb bytes.Buffer
		cmd.Stderr = &eb
		err = cmd.Run()
		_ = w.Close()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ws, _ = ee.Sys().(syscall.WaitStatus)
		} else if err != nil {
			t.Fatal(err)
		}
		return eb.String(), ws
	}
	t.Run("default disposition", func(t *testing.T) {
		stderr, ws := run(exe)
		if !ws.Signaled() || ws.Signal() != syscall.SIGPIPE {
			t.Fatalf("want death by SIGPIPE, got status %v stderr %q", ws, stderr)
		}
	})
	t.Run("ignored", func(t *testing.T) {
		stderr, ws := run("/bin/sh", "-c", `trap "" PIPE; exec "$0"`, exe)
		if ws.Signaled() || ws.ExitStatus() != 0 || stderr != "first broken pipe\nsecond ok\n" {
			t.Fatalf("status %v stderr %q, want the failure reported once and exit 0", ws, stderr)
		}
	})
}

// RB3: a standard output that is a non-blocking pipe loses nothing: the
// runtime waits for the reader.
func TestStdoutNonBlockingPipe(t *testing.T) {
	exe := buildProgram(t, floodProgram, native.Options{})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// the flag is on the open file description, which the child shares
	if err := syscall.SetNonblock(int(w.Fd()), true); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Stdout = w
	var eb bytes.Buffer
	cmd.Stderr = &eb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	time.Sleep(300 * time.Millisecond) // let the pipe fill and the writer wait
	data, _ := io.ReadAll(r)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v, stderr %q", err, eb.String())
	}
	if want := 20000 * 50; len(data) != want || eb.String() != "first ok\nsecond ok\n" {
		t.Fatalf("%d bytes arrived, want %d; stderr %q", len(data), want, eb.String())
	}
}

// RB3: a program stopped and continued in the middle of a large write
// loses nothing.
func TestStdoutSurvivesStopAndContinue(t *testing.T) {
	exe := buildProgram(t, floodProgram, native.Options{})
	cmd := exec.Command(exe)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var eb bytes.Buffer
	cmd.Stderr = &eb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGSTOP)
		time.Sleep(20 * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGCONT)
	}
	data, _ := io.ReadAll(stdout)
	if err := cmd.Wait(); err != nil || len(data) != 20000*50 {
		t.Fatalf("%v: %d bytes arrived, want %d; stderr %q", err, len(data), 20000*50, eb.String())
	}
}

// RB2 and the placeholder rule of specs/std-process R4: a program started
// with standard output closed writes into /dev/null and ends normally.
func TestStdoutClosedAtStart(t *testing.T) {
	exe := buildProgram(t, floodProgram, native.Options{})
	var eb bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", `exec "$0" >&-`, exe)
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil || eb.String() != "first ok\nsecond ok\n" {
		t.Fatalf("%v, stderr %q", err, eb.String())
	}
}

const quietProgram = "module t\nfunc main() -> i32 { return 0i32 }\n"

// RB1: each variable's accepted values, the line an unaccepted value
// earns, and the silence of an unset or empty one and of a privileged
// process.
func TestRuntimeConfigurationVariables(t *testing.T) {
	exe := buildProgram(t, quietProgram, native.Options{TestHooks: true})
	run := func(env ...string) string {
		t.Helper()
		cmd := exec.Command(exe)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		var eb bytes.Buffer
		cmd.Stderr = &eb
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v, stderr %q", env, err, eb.String())
		}
		return eb.String()
	}
	ignored := func(name string) string { return `{"runtime":{"ignored":"` + name + `"}}` + "\n" }
	cases := []struct {
		env  string
		want string
	}{
		{"KERN_STACK_BYTES=65536", ""},
		{"KERN_STACK_BYTES=1073741824", ""},
		{"KERN_STACK_BYTES=8388608", ""},
		{"KERN_STACK_BYTES=", ""},
		{"KERN_STACK_BYTES=65535", ignored("KERN_STACK_BYTES")},
		{"KERN_STACK_BYTES=1073741825", ignored("KERN_STACK_BYTES")},
		{"KERN_STACK_BYTES=+65536", ignored("KERN_STACK_BYTES")},
		{"KERN_STACK_BYTES= 65536", ignored("KERN_STACK_BYTES")},
		{"KERN_STACK_BYTES=65536k", ignored("KERN_STACK_BYTES")},
		{"KERN_STACK_BYTES=00000065536", ignored("KERN_STACK_BYTES")}, // eleven digits
		{"KERN_STACK_BYTES=0000065536", ""},                           // ten digits
		{"KERN_STACK_BYTES=abc", ignored("KERN_STACK_BYTES")},
		{"KERN_BACKTRACE=0", ""},
		{"KERN_BACKTRACE=1", ""},
		{"KERN_BACKTRACE=", ""},
		{"KERN_BACKTRACE=2", ignored("KERN_BACKTRACE")},
		{"KERN_BACKTRACE=true", ignored("KERN_BACKTRACE")},
		{"KERN_BACKTRACE=01", ignored("KERN_BACKTRACE")},
		{"KERN_ALLOC_REPORT=0", ""},
		{"KERN_ALLOC_REPORT=", ""},
		{"KERN_ALLOC_REPORT=yes", ignored("KERN_ALLOC_REPORT")},
	}
	for _, c := range cases {
		if got := run(c.env); got != c.want {
			t.Errorf("%s: stderr %q, want %q", c.env, got, c.want)
		}
	}
	if got := run(); got != "" {
		t.Errorf("no variable set: stderr %q", got)
	}
	// several unaccepted values: one line each, in the order the runtime reads them
	if got, want := run("KERN_ALLOC_REPORT=x", "KERN_STACK_BYTES=1", "KERN_BACKTRACE=x"), ignored("KERN_BACKTRACE")+ignored("KERN_STACK_BYTES")+ignored("KERN_ALLOC_REPORT"); got != want {
		t.Errorf("three unaccepted values: stderr %q, want %q", got, want)
	}
	// a privileged process reads none of them
	if got := run("KERN_TEST_PRIVILEGED=1", "KERN_STACK_BYTES=abc", "KERN_BACKTRACE=x"); got != "" {
		t.Errorf("a privileged process wrote %q", got)
	}
}

const exitProgram = `module t
import "std/io"
struct Loud nocopy { id: u64 }
impl Loud {
    destructor(edit self) { print_str("destroyed"); println() }
}
func spin() -> u64 {
    i := 0u64
    while true { i = i + 1u64; yield_now() }
    return i
}
func arg() -> Vec[u8] {
    a := io.args()
    if a.len() < 2usize { return Vec[u8].new() }
    return a.get(1usize).clone()
}
func is(a: view[]u8, b: view[]u8) -> bool {
    if len(a) != len(b) { return false }
    i := 0usize
    while i < len(a) {
        if a[i] != b[i] { return false }
        i = i + 1usize
    }
    return true
}
func main() -> i32 {
    mode := arg()
    l := Loud { id: 1u64 }
    print_str("before")
    println()
    if is(mode.as_view(), "exit256") { exit(256i32) }
    if is(mode.as_view(), "exit255") { exit(255i32) }
    if is(mode.as_view(), "exitm1") { exit(0i32 - 1i32) }
    if is(mode.as_view(), "exit3") { exit(3i32) }
    if is(mode.as_view(), "ret7") { return 7i32 }
    if is(mode.as_view(), "ret300") { return 300i32 }
    if is(mode.as_view(), "thread") {
        r := thread.spawn(spin)
        return 5i32
    }
    if is(mode.as_view(), "trap") { trap_kind(explicit) }
    return 0i32
}
`

// RB5: exit(code) and a return from main flush standard output and end the
// process with the low 8 bits of the value; exit runs no destructor; a
// trap ends the process by SIGABRT; a thread still running delays none of
// them.
func TestExitBehaviour(t *testing.T) {
	exe := buildProgram(t, exitProgram, native.Options{})
	cases := []struct {
		mode   string
		status int
		out    string
	}{
		{"none", 0, "before\ndestroyed\n"},
		{"ret7", 7, "before\ndestroyed\n"},
		{"ret300", 300 & 0xff, "before\ndestroyed\n"},
		{"exit3", 3, "before\n"},
		{"exit255", 255, "before\n"},
		{"exit256", 0, "before\n"},
		{"exitm1", 255, "before\n"},
		{"thread", 5, "before\ndestroyed\n"},
	}
	for _, c := range cases {
		cmd := exec.Command(exe, c.mode)
		var ob bytes.Buffer
		cmd.Stdout = &ob
		done := make(chan error, 1)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { done <- cmd.Wait() }()
		var err error
		select {
		case err = <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatalf("%s: the program did not end", c.mode)
		}
		status := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			status = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if status != c.status || ob.String() != c.out {
			t.Errorf("%s: status %d output %q, want %d %q", c.mode, status, ob.String(), c.status, c.out)
		}
	}
	cmd := exec.Command(exe, "trap")
	var ob, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &ob, &eb
	err := cmd.Run()
	var ee *exec.ExitError
	ws, _ := syscall.WaitStatus(0), false
	if errors.As(err, &ee) {
		ws, _ = ee.Sys().(syscall.WaitStatus)
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGABRT || ob.String() != "before\n" || !strings.Contains(eb.String(), `"kind":"explicit"`) {
		t.Errorf("trap: status %v output %q stderr %q, want SIGABRT after the flushed output and the record", ws, ob.String(), eb.String())
	}
}

// The standard streams as std/io's Reader and Writers (specs/runtime RG4):
// lines of standard input through a BufReader, standard output through
// Stdout (buffered, in order with print_str), standard error through
// Stderr (the system's write). A fixture cannot feed standard input or
// write standard error.
const streamsProgram = `module t
import "std/io"
func run() -> Result[(), io.IoErr] {
    r := io.BufReader[io.Stdin].new(io.stdin())
    so := io.stdout()
    er := io.stderr()
    while true {
        line := Vec[u8].new()
        if !r.read_line(line)? { break }
        io.write_to(so, line.as_view())?
        print_str("|")
    }
    println()
    n := er.write("to standard error\n")?
    er.flush()?
    print_u64(u64(n))
    println()
    so.flush()?
    return Ok(())
}
func main() -> i32 {
    match run() {
        Ok(u) => { }
        Err(e) => { print_str("failed"); println() }
    }
    return 0i32
}
`

func TestStandardStreamsAsReadersAndWriters(t *testing.T) {
	const input = "one\r\ntwo\n\nlast, with no end"
	const wantOut = "one|two||last, with no end|\n18\n"
	const wantErr = "to standard error\n"
	check := func(t *testing.T, cmd *exec.Cmd) {
		t.Helper()
		cmd.Stdin = strings.NewReader(input)
		var ob, eb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &ob, &eb
		if err := cmd.Run(); err != nil {
			t.Fatalf("run: %v, stderr %q", err, eb.String())
		}
		if ob.String() != wantOut || eb.String() != wantErr {
			t.Errorf("stdout %q, stderr %q; want %q and %q", ob.String(), eb.String(), wantOut, wantErr)
		}
	}
	t.Run("native", func(t *testing.T) {
		check(t, exec.Command(buildProgram(t, streamsProgram, native.Options{})))
	})
	t.Run("interpreter", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "t.kern")
		if err := os.WriteFile(src, []byte(streamsProgram), 0o644); err != nil {
			t.Fatal(err)
		}
		check(t, exec.Command(kernBinary(t), "run", src))
	})
}
