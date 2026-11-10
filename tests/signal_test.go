package tests

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
