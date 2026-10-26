package tests

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/source"
)

// runNative builds src natively and runs it with a 5 s budget, returning
// stdout, stderr, whether it died by SIGABRT, and whether it timed out.
func runNative(t *testing.T, name, src string) (out, errb string, aborted, timedOut bool) {
	t.Helper()
	if !native.Available() {
		t.Skip("clang not available")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	f := &source.File{Name: name, Bytes: []byte(src)}
	ds, err := native.Build([]*source.File{f}, exe, native.Options{})
	if err != nil {
		t.Fatalf("native build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("diagnostics: %s", render(ds))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	var ob, eb bytes.Buffer
	cmd.Stdout = &ob
	cmd.Stderr = &eb
	err = cmd.Run()
	if ctx.Err() != nil {
		return ob.String(), eb.String(), false, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGABRT {
			aborted = true
		}
	}
	return ob.String(), eb.String(), aborted, false
}

func records(stderr string) int {
	return strings.Count(stderr, `{"trap":`)
}

const handlerPrelude = `module t
func report(info: view[TrapInfo]) {
    print_str("handler ")
    match info.kind {
        stack => print_str("stack")
        overflow => print_str("overflow")
        bounds => print_str("bounds")
        _ => print_str("other")
    }
    println()
}
`

// A conforming handler after a stack trap taken while the thread holds a
// Mutex and buffered output is pending: the handler's output appears and the
// process dies by SIGABRT (PLAN.md 2.2).
func TestNativeStackTrapUnderMutexWithHandler(t *testing.T) {
	src := handlerPrelude + `
func down(n: u64) -> u64 { return down(n + 1u64) + 1u64 }
func main() -> i32 {
    prev := trap.set_handler(report)
    m := Mutex[u64].new(1u64)
    print_str("before")
    with c := m.lock() {
        c = down(c)
    }
    return 0i32
}
`
	out, errb, aborted, timedOut := runNative(t, "mutex_stack.kern", src)
	if timedOut || !aborted {
		t.Fatalf("expected SIGABRT within 5 s, timed out=%v aborted=%v stderr=%q", timedOut, aborted, errb)
	}
	if !strings.Contains(out, "handler stack") || !strings.Contains(out, "before") {
		t.Fatalf("handler output missing: %q", out)
	}
	if records(errb) != 0 {
		t.Fatalf("custom handler selected, but a default record was written: %q", errb)
	}
}

// A non-conforming handler demonstrates the obligation (ADR-012) rather
// than an enforced rule: it allocates (a Vec push) after a stack trap taken
// inside an allocation, so the runtime may deadlock on the allocator lock or
// trap a second time; either ends the process, and memory safety holds. The
// plan's variant that re-locks the held Mutex is not expressible in safe
// kern: second-class access keeps a held Mutex unreachable from a handler.
func TestNativeNonConformingHandler(t *testing.T) {
	src := `module t
func report(info: view[TrapInfo]) {
    v := Vec[u64].new()
    v.push(1u64)
    print_str("in handler")
    println()
}
func down(v: edit[Vec[u64]], n: u64) -> u64 {
    v.push(n)
    return down(v, n + 1u64) + 1u64
}
func main() -> i32 {
    prev := trap.set_handler(report)
    v := Vec[u64].new()
    x := down(v, 0u64)
    return 0i32
}
`
	_, _, aborted, timedOut := runNative(t, "nonconforming.kern", src)
	if !aborted && !timedOut {
		t.Fatalf("expected the demonstrated outcome (deadlock or SIGABRT)")
	}
	t.Logf("non-conforming handler outcome: aborted=%v timedOut=%v (documented, not enforced)", aborted, timedOut)
}

// A nested trap under the default handler writes exactly one record.
func TestNativeNestedTrapDefaultHandler(t *testing.T) {
	src := `module t
func main() -> i32 {
    x := 255u8
    y := x + 1u8
    return i32(y)
}
`
	_, errb, aborted, _ := runNative(t, "nested_default.kern", src)
	if !aborted || records(errb) != 1 {
		t.Fatalf("expected SIGABRT and one record, aborted=%v stderr=%q", aborted, errb)
	}
}

// Two threads overflowing concurrently: the process dies by SIGABRT and at
// least one record is written; records never interleave within a line.
func TestNativeConcurrentTraps(t *testing.T) {
	src := `module t
func boom(n: u64) -> u64 {
    x := 255u8
    y := x + u8(n)
    return u64(y)
}
func run() -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        j1 := s.spawn(boom, 1u64)?
        j2 := s.spawn(boom, 1u64)?
        return Ok(j1.get() + j2.get())
    }
}
func main() -> i32 {
    match run() {
        Ok(v) => { print_u64(v); println() }
        Err(e) => { print_str("spawn failed"); println() }
    }
    return 0i32
}
`
	_, errb, aborted, timedOut := runNative(t, "concurrent.kern", src)
	if timedOut || !aborted || records(errb) < 1 {
		t.Fatalf("expected SIGABRT with a record, timedOut=%v aborted=%v stderr=%q", timedOut, aborted, errb)
	}
	for _, line := range strings.Split(strings.TrimSpace(errb), "\n") {
		if !strings.HasPrefix(line, `{"trap":`) || !strings.HasSuffix(line, "}}") {
			t.Fatalf("malformed record line %q", line)
		}
	}
}

// Overflow of the stack during an allocation (every frame pushes) is still
// classified as a stack trap.
func TestNativeStackTrapDuringAllocation(t *testing.T) {
	src := `module t
func down(v: edit[Vec[u64]], n: u64) -> u64 {
    v.push(n)
    return down(v, n + 1u64) + 1u64
}
func main() -> i32 {
    v := Vec[u64].new()
    x := down(v, 0u64)
    return 0i32
}
`
	_, errb, aborted, _ := runNative(t, "alloc_stack.kern", src)
	if !aborted || !strings.Contains(errb, `"kind":"stack"`) {
		t.Fatalf("expected a stack trap record, aborted=%v stderr=%q", aborted, errb)
	}
}

// Handler replacement concurrent with traps: each trap runs exactly one
// handler (the atomic exchange and load of spec §6), so the output names one
// handler per trapping thread and the process dies by SIGABRT.
func TestNativeHandlerReplacementUnderTraps(t *testing.T) {
	src := `module t
func h1(info: view[TrapInfo]) { print_str("h1"); println() }
func h2(info: view[TrapInfo]) { print_str("h2"); println() }
func boom(n: u64) -> u64 {
    x := 255u8
    y := x + u8(n)
    return u64(y)
}
func swap(n: u64) -> u64 {
    i := 0u64
    while i < n { prev := trap.set_handler(h2); prev2 := trap.set_handler(h1); i = i + 1u64 }
    return i
}
func run() -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        j1 := s.spawn(swap, 100000u64)?
        j2 := s.spawn(boom, 1u64)?
        return Ok(j1.get() + j2.get())
    }
}
func main() -> i32 {
    prev := trap.set_handler(h1)
    match run() {
        Ok(v) => { print_u64(v); println() }
        Err(e) => { print_str("spawn failed"); println() }
    }
    return 0i32
}
`
	out, errb, aborted, timedOut := runNative(t, "replace.kern", src)
	if timedOut || !aborted {
		t.Fatalf("expected SIGABRT, timedOut=%v aborted=%v stderr=%q", timedOut, aborted, errb)
	}
	n := strings.Count(out, "h1\n") + strings.Count(out, "h2\n")
	if n != 1 || records(errb) != 0 {
		t.Fatalf("expected exactly one handler run and no default record, out=%q stderr=%q", out, errb)
	}
}
