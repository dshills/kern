package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/source"
)

// nativeRecord builds src at the opt level, runs it, and returns the trap
// record it wrote on standard error.
func nativeRecord(t *testing.T, name, src, opt string) locRecord {
	t.Helper()
	if !native.Available() {
		t.Skip("clang not available")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	ds, err := native.Build([]*source.File{{Name: name, Bytes: []byte(src)}}, exe, native.Options{Opt: opt})
	if err != nil {
		t.Fatalf("native build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("diagnostics: %s", render(ds))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	err = cmd.Run()
	// the handler reads only memory it knows is mapped: a second fault
	// would kill the process by SIGSEGV instead of SIGABRT
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("the program did not fail: %v", err)
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGABRT {
		t.Fatalf("the program did not die by SIGABRT: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(eb.String()), "\n")
	var r struct{ Trap locRecord }
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &r); err != nil {
		t.Fatalf("no trap record in %q", eb.String())
	}
	return r.Trap
}

type locRecord struct {
	Kind string
	File string
	Span struct{ Start, End int }
}

// declSpan is the span of the declaration of function fn in src: from
// `func fn` to the closing brace that ends it at the start of a line.
func declSpan(src, fn string) (int, int) {
	at := strings.Index(src, "func "+fn+"(")
	end := strings.Index(src[at:], "\n}\n")
	if end < 0 {
		end = strings.Index(src[at:], "}\n") - 1
	}
	return at, at + end + 2
}

// A native stack trap, or a fault in unsafe code, names the user function
// it happened in: its declaration's span and file, as the interpreter
// names the function whose activation exceeded the budget
// (specs/native-trap-loc/SPEC.md). A library frame (here Vec.push) is
// passed over to its user-level caller, so is the runtime's C code, and a
// spawned thread's trap is located in its own stack.
func TestNativeTrapLocations(t *testing.T) {
	cases := []struct {
		name, src, kind string
		fns             []string // the user functions the record may name
	}{
		{"deep.kern", `module t
func deep(n: u64) -> u64 {
    var buf: [32]u64 = zero()
    buf[usize(n & 31u64)] = n
    return deep(n + 1u64) + buf[1usize]
}
func main() -> i32 {
    print_u64(deep(0u64))
    return 0i32
}
`, "stack", []string{"deep"}},
		{"pushing.kern", `module t
func down(v: edit[Vec[u64]], n: u64) -> u64 {
    v.push(n)
    return down(v, n + 1u64) + 1u64
}
func main() -> i32 {
    v := Vec[u64].new()
    print_u64(down(v, 0u64))
    return 0i32
}
`, "stack", []string{"down"}},
		{"spawned.kern", `module t
func down(n: u64) -> u64 {
    var buf: [32]u64 = zero()
    buf[usize(n & 31u64)] = n
    return down(n + 1u64) + buf[1usize]
}
func run() -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        j := s.spawn(down, 1u64)?
        return Ok(j.get())
    }
}
func main() -> i32 {
    match run() {
        Ok(v) => { print_u64(v) }
        Err(e) => { print_str("spawn failed") }
    }
    return 0i32
}
`, "stack", []string{"down"}},
		// the overflow happens in the runtime's C code (kern_io_remove has a
		// 4 KB frame), found through the frame chain
		{"through_c.kern", `module t
import "std/io"
func down(p: view[]u8, n: u64) -> u64 {
    match io.remove_file(p) {
        Ok(u) => { return 0u64 }
        Err(e) => { return down(p, n + 1u64) + 1u64 }
    }
}
func main() -> i32 {
    print_u64(down("/nonexistent/kern-probe", 0u64))
    return 0i32
}
`, "stack", []string{"down"}},
		{"fault.kern", `module t
contract { requires "nothing: the program faults on purpose" }
func peek(a: usize) -> u64 {
    unsafe { return ptr_load(ptr_from_addr[u64](a)) }
}
func main() -> i32 {
    print_u64(peek(16usize))
    return 0i32
}
`, "unreachable", []string{"peek", "main"}}, // peek may be inlined into main
	}
	for _, c := range cases {
		for _, opt := range []string{"debug", "release"} {
			t.Run(c.name+"/"+opt, func(t *testing.T) {
				r := nativeRecord(t, c.name, c.src, opt)
				if r.Kind != c.kind || r.File != c.name {
					t.Fatalf("record %+v, want kind %s in %s", r, c.kind, c.name)
				}
				for _, fn := range c.fns {
					if s, e := declSpan(c.src, fn); r.Span.Start == s && r.Span.End == e {
						return
					}
				}
				t.Errorf("span %d-%d names none of %v", r.Span.Start, r.Span.End, c.fns)
			})
		}
	}
}

// A stack overflow during allocation dies by SIGABRT with a located
// record every time: the fault handler reads only what it knows it can,
// so no run faults again inside it (specs/native-trap-loc R3). Repeated
// because an address layout that breaks a read shows in some runs only;
// CI runs it on x86-64 and arm64 Linux and on macOS.
func TestNativeStackTrapRepeated(t *testing.T) {
	if !native.Available() {
		t.Skip("clang not available")
	}
	src := `module t
func down(v: edit[Vec[u64]], n: u64) -> u64 {
    v.push(n)
    return down(v, n + 1u64) + 1u64
}
func main() -> i32 {
    v := Vec[u64].new()
    print_u64(down(v, 0u64))
    return 0i32
}
`
	for _, opt := range []string{"debug", "release"} {
		exe := filepath.Join(t.TempDir(), "prog")
		ds, err := native.Build([]*source.File{{Name: "push.kern", Bytes: []byte(src)}}, exe, native.Options{Opt: opt})
		if err != nil || len(ds) > 0 {
			t.Fatalf("native build: %v %v", err, ds)
		}
		start, end := declSpan(src, "down")
		for i := 0; i < 20; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			cmd := exec.CommandContext(ctx, exe)
			var eb bytes.Buffer
			cmd.Stderr = &eb
			err := cmd.Run()
			cancel()
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("%s run %d: the program did not fail: %v", opt, i, err)
			}
			if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGABRT {
				t.Fatalf("%s run %d: died by %v, not SIGABRT; stderr %q", opt, i, err, eb.String())
			}
			want := fmt.Sprintf(`"span":{"start":%d,"end":%d}`, start, end)
			if !strings.Contains(eb.String(), want) {
				t.Fatalf("%s run %d: record %q does not name down (%s)", opt, i, eb.String(), want)
			}
		}
	}
}
