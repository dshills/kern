package tests

import (
	"bufio"
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
)

// std/signal as a process outside the program sees it (specs/runtime
// RI1): signals sent to a running program, which a fixture cannot do.

// A program that catches three signals and prints each as it arrives.
const signalWaiter = `module t
import "std/signal"
import "std/io"
func say(s: view[]u8) {
    print_str(s)
    println()
    match io.flush_stdout() {
        Ok(u) => { }
        Err(e) => { }
    }
}
func main() -> i32 {
    with sigs := signal.catch(signal.Signal.Terminate) {
        sigs.add(signal.Signal.Interrupt)
        sigs.add(signal.Signal.Hangup)
        say("ready")
        for i in 0u64..3u64 {
            match sigs.wait() {
                Interrupt => say("Interrupt")
                Terminate => say("Terminate")
                Hangup => say("Hangup")
                _ => say("another signal")
            }
        }
    }
    return 0i32
}
`

// A program that ignores Terminate until it is told to go on, then gives
// it the system's default again and waits.
const signalDispositions = `module t
import "std/signal"
import "std/io"
func say(s: view[]u8) {
    print_str(s)
    println()
    match io.flush_stdout() {
        Ok(u) => { }
        Err(e) => { }
    }
}
func line() {
    input := io.stdin()
    var b: [1]u8 = zero()
    match input.read(b) {
        Ok(n) => { }
        Err(e) => { }
    }
}
func main() -> i32 {
    signal.ignore(signal.Signal.Terminate)
    say("ignoring")
    line()
    signal.reset(signal.Signal.Terminate)
    say("default")
    line()
    say("still running")
    return 0i32
}
`

// started runs a program natively or in the interpreter and returns the
// command with its pipes.
func started(t *testing.T, src string, interpreted bool) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	var cmd *exec.Cmd
	if interpreted {
		file := filepath.Join(t.TempDir(), "t.kern")
		if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd = exec.Command(kernBinary(t), "run", file)
	} else {
		cmd = exec.Command(buildProgram(t, src, native.Options{}))
	}
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
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, stdin, bufio.NewReader(stdout)
}

// expectLine reads the program's next line, giving it ten seconds.
func expectLine(t *testing.T, r *bufio.Reader, want string) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		s, _ := r.ReadString('\n')
		got <- s
	}()
	select {
	case s := <-got:
		if s != want+"\n" {
			t.Fatalf("the program printed %q, want %q", s, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the program did not print %q in 10 s", want)
	}
}

func TestSignalsFromOutside(t *testing.T) {
	for _, engine := range []string{"native", "interpreter"} {
		t.Run(engine, func(t *testing.T) {
			cmd, _, out := started(t, signalWaiter, engine == "interpreter")
			expectLine(t, out, "ready")
			for _, s := range []struct {
				sig  syscall.Signal
				name string
			}{{syscall.SIGTERM, "Terminate"}, {syscall.SIGHUP, "Hangup"}, {syscall.SIGINT, "Interrupt"}} {
				if err := cmd.Process.Signal(s.sig); err != nil {
					t.Fatal(err)
				}
				expectLine(t, out, s.name)
			}
			if err := cmd.Wait(); err != nil {
				t.Errorf("the program did not end in order: %v", err)
			}
		})
	}
}

func TestSignalIgnoredThenDefault(t *testing.T) {
	for _, engine := range []string{"native", "interpreter"} {
		t.Run(engine, func(t *testing.T) {
			cmd, in, out := started(t, signalDispositions, engine == "interpreter")
			expectLine(t, out, "ignoring")
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			time.Sleep(100 * time.Millisecond) // time for an unignored signal to end the program
			if _, err := in.Write([]byte("\n")); err != nil {
				t.Fatalf("the program is gone: %v", err)
			}
			expectLine(t, out, "default") // it survived the ignored signal
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("the program ended with %v, want death by the signal", err)
			}
			ws, ok := ee.Sys().(syscall.WaitStatus)
			if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
				t.Errorf("the program ended with %v, want death by SIGTERM", err)
			}
		})
	}
}

// A program that raises a signal on itself: which one, and with which
// disposition, its number of arguments chooses.
const signalRaiser = `module t
import "std/signal"
import "std/io"
func say(s: view[]u8) { print_str(s); println() }
func main() -> i32 {
    say("before")
    a := io.args()
    n := a.len()
    if n == 1usize { signal.raise(signal.Signal.Terminate) }
    if n == 2usize { signal.raise(signal.Signal.User1) }
    if n == 3usize { signal.raise(signal.Signal.Quit) }
    if n == 4usize { signal.raise(signal.Signal.Pipe) }
    if n == 5usize {
        signal.ignore(signal.Signal.User2)
        signal.raise(signal.Signal.User2)
        say("ignored")
        signal.reset(signal.Signal.User2)
        signal.raise(signal.Signal.User2)
    }
    if n == 6usize {
        signal.raise(signal.Signal.Child)
        say("a child signal does nothing")
        signal.raise(signal.Signal.Hangup)
    }
    say("after")
    return 0i32
}
`

// A signal at its default disposition does to an interpreted program what
// the system does to a native one (specs/runtime RM6): the process dies
// by that signal, after what it had written. The Go runtime the
// interpreter runs on would let User1, User2 and Pipe pass and answer
// Quit with a dump of its own.
func TestSignalDefaultsEndTheProgram(t *testing.T) {
	cases := []struct {
		args int
		sig  syscall.Signal
		out  string
	}{
		{0, syscall.SIGTERM, "before\n"},
		{1, syscall.SIGUSR1, "before\n"},
		{2, syscall.SIGQUIT, "before\n"},
		{3, syscall.SIGPIPE, "before\n"},
		{4, syscall.SIGUSR2, "before\nignored\n"},
		{5, syscall.SIGHUP, "before\na child signal does nothing\n"},
	}
	for _, engine := range []string{"native", "interpreter"} {
		t.Run(engine, func(t *testing.T) {
			var argv []string
			if engine == "interpreter" {
				file := filepath.Join(t.TempDir(), "t.kern")
				if err := os.WriteFile(file, []byte(signalRaiser), 0o644); err != nil {
					t.Fatal(err)
				}
				argv = []string{kernBinary(t), "run", file, "--"}
			} else {
				argv = []string{buildProgram(t, signalRaiser, native.Options{})}
			}
			for _, c := range cases {
				args := append([]string{}, argv[1:]...)
				for i := 0; i < c.args; i++ {
					args = append(args, "x")
				}
				cmd := exec.Command(argv[0], args...)
				var stderr strings.Builder
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if string(out) != c.out {
					t.Errorf("%v: the program wrote %q, want %q", c.sig, out, c.out)
				}
				if stderr.Len() != 0 {
					t.Errorf("%v: the program wrote to standard error: %s", c.sig, stderr.String())
				}
				var ee *exec.ExitError
				if !errors.As(err, &ee) {
					t.Errorf("%v: the program ended with %v, want death by the signal", c.sig, err)
					continue
				}
				if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != c.sig {
					t.Errorf("%v: the program ended with %v, want death by the signal", c.sig, err)
				}
			}
		})
	}
}

// A program that catches User1 once, then gives it up and waits.
const signalUserWaiter = `module t
import "std/signal"
import "std/io"
func say(s: view[]u8) {
    print_str(s)
    println()
    match io.flush_stdout() {
        Ok(u) => { }
        Err(e) => { }
    }
}
func line() {
    input := io.stdin()
    var b: [1]u8 = zero()
    match input.read(b) {
        Ok(n) => { }
        Err(e) => { }
    }
}
func main() -> i32 {
    with sigs := signal.catch(signal.Signal.User1) {
        say("ready")
        match sigs.wait() {
            User1 => say("User1")
            _ => say("another signal")
        }
    }
    say("default")
    line()
    say("still running")
    return 0i32
}
`

// The same from outside the program: a caught User1 is the program's to
// handle, and once it is no longer caught the next one ends the program,
// natively and interpreted alike.
func TestSignalDefaultFromOutside(t *testing.T) {
	for _, engine := range []string{"native", "interpreter"} {
		t.Run(engine, func(t *testing.T) {
			cmd, _, out := started(t, signalUserWaiter, engine == "interpreter")
			expectLine(t, out, "ready")
			if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
				t.Fatal(err)
			}
			expectLine(t, out, "User1")
			expectLine(t, out, "default")
			if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("the program ended with %v, want death by the signal", err)
			}
			if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGUSR1 {
				t.Errorf("the program ended with %v, want death by SIGUSR1", err)
			}
		})
	}
}
