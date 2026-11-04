package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// timeoutEngines builds the kern binary and returns, per engine, the
// command line that runs src, with MARKER replaced by marker: `kern run` (the interpreter) and the native
// executable `kern build` makes. The native one is absent without clang.
func timeoutEngines(t *testing.T, src, marker string) map[string][]string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the kern binary")
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "kern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	prog := filepath.Join(tmp, "prog.kern")
	if err := os.WriteFile(prog, []byte(strings.ReplaceAll(src, "MARKER", marker)), 0o644); err != nil {
		t.Fatal(err)
	}
	engines := map[string][]string{"interpreter": {bin, "run", prog}}
	if _, err := exec.LookPath("clang"); err == nil {
		exe := filepath.Join(tmp, "prog")
		if out, err := exec.Command(bin, "build", "-o", exe, prog).CombinedOutput(); err != nil {
			t.Fatalf("kern build: %v\n%s", err, out)
		}
		engines["native"] = []string{exe}
	}
	return engines
}

// waitFor polls until path exists or 20 s pass.
func waitFor(t *testing.T, path string) {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s never appeared", path)
}

// read_stdin_timeout on a pipe that stays open and empty is None; once
// bytes arrive a timed read returns them; a plain read_stdin after the
// timed-out read loses nothing; at end of file a timed read is Some(0)
// (specs/std-timeouts R5). The program leaves a marker file before its
// first read, and the test writes only after the marker and a second.
func TestReadStdinTimeout(t *testing.T) {
	src := `module t
import "std/io"
import "std/time"
func timed(ms: u64) {
    var buf: [8]u8 = zero()
    r := io.read_stdin_timeout(buf, time.millis(ms))
    match r {
        Ok(x) => {
            match x {
                Some(n) => {
                    print_str("some ")
                    print_u64(u64(n))
                    print_str(" [")
                    print_str(buf[0usize : n])
                    print_str("]")
                }
                None => print_str("none")
            }
        }
        Err(e) => print_str("error")
    }
    println()
}
func main() -> i32 {
    w := io.write_file("MARKER", "ready")
    timed(200u64)
    timed(20000u64)
    with rest := io.read_stdin() {
        match rest {
            Ok(v) => {
                print_str("rest [")
                print_str(v.as_view())
                print_str("]")
                println()
            }
            Err(e) => { print_str("error"); println() }
        }
    }
    timed(20000u64)
    return 0i32
}
`
	marker := filepath.Join(t.TempDir(), "ready")
	for name, argv := range timeoutEngines(t, src, marker) {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(marker)
			cmd := exec.Command(argv[0], argv[1:]...)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// a watchdog, and no program outlives its test, whatever fails
			watchdog := time.AfterFunc(60*time.Second, func() { _ = cmd.Process.Kill() })
			t.Cleanup(func() {
				watchdog.Stop()
				_ = cmd.Process.Kill()
				_ = in.Close()
			})
			waitFor(t, marker)
			time.Sleep(time.Second)
			if _, err := in.Write([]byte("abc")); err != nil {
				t.Fatal(err)
			}
			time.Sleep(500 * time.Millisecond)
			if _, err := in.Write([]byte("def")); err != nil {
				t.Fatal(err)
			}
			_ = in.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			if want := "none\nsome 3 [abc]\nrest [def]\nsome 0 []\n"; out.String() != want {
				t.Errorf("got\n%s\nwant\n%s", out.String(), want)
			}
		})
	}
}

// childrenOf lists the processes whose parent is pid, zombies included.
func childrenOf(t *testing.T, pid int) []string {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,stat=,comm=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var kids []string
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && f[1] == strconv.Itoa(pid) {
			kids = append(kids, l)
		}
	}
	return kids
}

// After output_timeout's deadline the child is killed and reaped: the
// program then has no child, running or zombie (specs/std-timeouts R4).
// The program leaves a marker and blocks on its input while the test
// looks.
func TestOutputTimeoutReaps(t *testing.T) {
	src := `module t
import "std/io"
import "std/process"
import "std/time"
func main() -> i32 {
    c := process.Command.new("sleep")
    c.arg("30")
    with r := c.output_timeout("", time.millis(200u64)) {
        match r {
            Ok(x) => {
                match x {
                    Some(o) => print_str("some")
                    None => print_str("none")
                }
            }
            Err(e) => print_str("error")
        }
    }
    println()
    w := io.write_file("MARKER", "ready")
    with rest := io.read_stdin() { }
    return 0i32
}
`
	marker := filepath.Join(t.TempDir(), "ready")
	for name, argv := range timeoutEngines(t, src, marker) {
		t.Run(name, func(t *testing.T) {
			_ = os.Remove(marker)
			cmd := exec.Command(argv[0], argv[1:]...)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			cmd.Stdout = &out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// a watchdog, and no program outlives its test, whatever fails
			watchdog := time.AfterFunc(60*time.Second, func() { _ = cmd.Process.Kill() })
			t.Cleanup(func() {
				watchdog.Stop()
				_ = cmd.Process.Kill()
				_ = in.Close()
			})
			start := time.Now()
			waitFor(t, marker)
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("output_timeout took %v", d)
			}
			kids := childrenOf(t, cmd.Process.Pid)
			_ = in.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("%v\n%s", err, out.String())
			}
			if len(kids) > 0 {
				t.Errorf("children after output_timeout: %v", kids)
			}
			if out.String() != "none\n" {
				t.Errorf("output %q", out.String())
			}
		})
	}
}
