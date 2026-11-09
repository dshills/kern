package tests

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/dshills/kern/compiler/backend/native"
)

// Stack sizes as a process around the program sees them (specs/runtime
// RC2): the build's value, the environment's and the program's own, for a
// spawned thread and for main.

// deepProgram recurses to a depth its first argument's letter selects, on
// a spawned thread or on main, in frames of about 1 KiB. "s": 3,000 frames
// (about 3 MiB) on a spawned thread; "m": the same on main; "M": 20,000
// frames (about 20 MiB) on main; "a": set_stack_bytes(16 MiB), then "s".
const deepProgram = `module t
import "std/io"
func deep(n: u64) -> u64 {
    var pad: [120]u64 = zero()
    pad[usize(n % 120u64)] = n
    if n == 0u64 { return pad[0usize] }
    return deep(n - 1u64) + pad[usize((n + 1u64) % 120u64)]
}
func three_thousand() -> u64 { return deep(3000u64) }
func on_thread() -> Result[u64, SpawnErr] {
    t := thread.spawn(three_thousand)?
    return Ok(t.join())
}
func mode() -> u8 {
    a := io.args()
    if a.len() < 2usize { return 0u8 }
    with first := a.at(1usize) {
        if first.len() == 0usize { return 0u8 }
        return first.get(0usize)
    }
}
func main() -> i32 {
    m := mode()
    if m == 97u8 {
        if !thread.set_stack_bytes(16777216usize) { print_str("refused"); println() }
        m = 115u8
    }
    if m == 115u8 {
        r := on_thread()
        match r {
            Ok(v) => { print_str("thread ok"); println() }
            Err(e) => { print_str("spawn failed"); println() }
        }
    }
    if m == 109u8 { x := deep(3000u64); print_str("main ok"); println() }
    if m == 77u8 { x := deep(20000u64); print_str("main ok"); println() }
    return 0i32
}
`

// runDeep runs a build of deepProgram and reports its output and whether it
// ended in a stack trap.
func runDeep(t *testing.T, exe, mode string, env ...string) (out string, stackTrap bool) {
	t.Helper()
	cmd := exec.Command(exe, mode)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var ob, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &ob, &eb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		ws, _ := ee.Sys().(syscall.WaitStatus)
		if ws.Signaled() && ws.Signal() == syscall.SIGABRT && strings.Contains(eb.String(), `"kind":"stack"`) {
			return ob.String(), true
		}
		t.Fatalf("mode %s %v: %v, stderr %q", mode, env, err, eb.String())
	} else if err != nil {
		t.Fatal(err)
	}
	return ob.String(), false
}

func TestStackBytes(t *testing.T) {
	plain := buildProgram(t, deepProgram, native.Options{})
	small := buildProgram(t, deepProgram, native.Options{StackBytes: 1 << 20})
	large := buildProgram(t, deepProgram, native.Options{StackBytes: 64 << 20})
	cases := []struct {
		name  string
		exe   string
		mode  string
		env   []string
		out   string
		traps bool
	}{
		{"default: a spawned thread has 8 MiB", plain, "s", nil, "thread ok\n", false},
		{"default: main on the initial stack", plain, "m", nil, "main ok\n", false},
		{"build 1 MiB: a spawned thread overflows", small, "s", nil, "", true},
		{"build 1 MiB: main overflows too", small, "m", nil, "", true},
		{"build 64 MiB: main recurses 20 MiB", large, "M", nil, "main ok\n", false},
		{"the environment overrides the build, upward", small, "s", []string{"KERN_STACK_BYTES=16777216"}, "thread ok\n", false},
		{"the environment overrides the build, for main", small, "m", []string{"KERN_STACK_BYTES=16777216"}, "main ok\n", false},
		{"the environment overrides the build, downward", large, "s", []string{"KERN_STACK_BYTES=1048576"}, "", true},
		{"the environment alone moves main", plain, "M", []string{"KERN_STACK_BYTES=67108864"}, "main ok\n", false},
		{"an unaccepted environment value changes nothing", small, "s", []string{"KERN_STACK_BYTES=lots"}, "", true},
		{"the program's own setting overrides both", small, "a", []string{"KERN_STACK_BYTES=1048576"}, "thread ok\n", false},
	}
	for _, c := range cases {
		out, traps := runDeep(t, c.exe, c.mode, c.env...)
		if out != c.out || traps != c.traps {
			t.Errorf("%s: output %q stack trap %v, want %q %v", c.name, out, traps, c.out, c.traps)
		}
	}
}

// RC2: when the thread for main cannot be created, the runtime says so and
// ends with status 134 without running main. Only Linux lets a test refuse
// the stack (an address-space limit); macOS has no such limit to set.
func TestMainStackUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs an address-space limit (ulimit -v), which only Linux enforces")
	}
	exe := buildProgram(t, deepProgram, native.Options{})
	cmd := exec.Command("/bin/sh", "-c", `ulimit -v 262144; exec "$0" m`, exe)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "KERN_STACK_BYTES=1073741824"}
	var ob, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &ob, &eb
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 134 || eb.String() != "{\"runtime\":{\"fatal\":\"main stack\"}}\n" || ob.Len() != 0 {
		t.Fatalf("want status 134 and the fatal line with main not run; got %v, stdout %q, stderr %q", err, ob.String(), eb.String())
	}
}
