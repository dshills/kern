package analysis_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
)

// hosted runs the hosted-only evaluation on a lowered module for the
// AArch64 freestanding target and renders each diagnostic as
// `quoted source: message`.
func hostedMod(t *testing.T, src string, mod *ir.Module) (analysis.HostedResult, []string) {
	t.Helper()
	res, err := analysis.HostedOnly(mod, "aarch64-unknown-none-elf")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range res.Diagnostics {
		if d.Code != "ENV_HOSTED_ONLY" || d.Fix != nil || len(d.Related) != 0 || d.File != "m.kern" || d.Rule != "6.8" {
			t.Errorf("diagnostic %+v: want ENV_HOSTED_ONLY (6.8) in m.kern, no fix, no related", d)
		}
		out = append(out, src[d.Span.Start:d.Span.End]+": "+d.Message)
	}
	return res, out
}

func hosted(t *testing.T, src, level string) (analysis.HostedResult, []string) {
	t.Helper()
	return hostedMod(t, src, buildBudgeted(t, map[string]string{"m.kern": src}, level).mod)
}

func msg(call, name, entry string) string {
	return call + ": call to `" + name + "` needs the hosted runtime (`" + entry + "`), which a freestanding image does not have"
}

// The three programs of specs/freestanding-availability R1, at both
// levels: a library call, a prelude intrinsic called directly, and a
// library call inside a function of the program's own.
func TestHostedOnlyR1(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{`module m
import "std/time"
func main() -> i32 {
    t := time.wall_nanos()
    print_u64(t)
    return 0i32
}
`, []string{msg("time.wall_nanos()", "time.wall_nanos", "kern_time_wall_ns")}},
		{`module m
import "std/io"
func main() -> i32 {
    print_u64(u64(io.io_argc()))
    return 0i32
}
`, []string{msg("io.io_argc()", "io_argc", "kern_io_argc")}},
		{`module m
import "std/time"
func nap() { print_u64(time.wall_nanos()) }
func main() -> i32 {
    nap()
    return 0i32
}
`, []string{msg("time.wall_nanos()", "time.wall_nanos", "kern_time_wall_ns")}},
	} {
		for _, level := range []string{"debug", "release"} {
			if _, got := hosted(t, tc.src, level); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s:\n got %q\nwant %q\n%s", level, got, tc.want, tc.src)
			}
		}
	}
}

// What the freestanding runtime provides (console output, the allocator
// behind the containers) and a module imported but not called give
// nothing.
func TestHostedOnlyNone(t *testing.T) {
	for _, src := range []string{`module m
func main() -> i32 {
    v := Vec[u64].new()
    v.push(3u64)
    print_u64(u64(v.len()))
    println()
    return 0i32
}
`, `module m
import "std/time"
func main() -> i32 { return 0i32 }
`} {
		for _, level := range []string{"debug", "release"} {
			if res, got := hosted(t, src, level); len(got) != 0 || len(res.Entries) != 0 {
				t.Errorf("%s: %q, entries %v\n%s", level, got, res.Entries, src)
			}
		}
	}
}

// Which calls are reaching (§1): a library function referenced as a
// value is reported at the reference and its indirect call adds nothing;
// a closure of the program's own is reported at the call inside it, not
// where it is passed or called; an uncalled function, a destructor and a
// call under `if false` are reported (roots, F2); the instantiations of a
// generic function give one diagnostic.
func TestHostedOnlyReaching(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{"reference", `module m
import "std/time"
func call(f: func() -> u64) -> u64 { return f() }
func main() -> i32 {
    print_u64(call(time.wall_nanos))
    return 0i32
}
`, []string{msg("time.wall_nanos", "time.wall_nanos", "kern_time_wall_ns")}},
		{"closure", `module m
import "std/time"
func run(g: closure() -> u64) -> u64 { return g() }
func main() -> i32 {
    print_u64(run(|| time.wall_nanos()))
    return 0i32
}
`, []string{msg("time.wall_nanos()", "time.wall_nanos", "kern_time_wall_ns")}},
		{"uncalled", `module m
import "std/time"
func unused() -> u64 { return time.wall_nanos() }
func main() -> i32 { return 0i32 }
`, []string{msg("time.wall_nanos()", "time.wall_nanos", "kern_time_wall_ns")}},
		{"destructor", `module m
import "std/time"
struct Tok nocopy { id: u64 }
impl Tok {
    destructor(edit self) { print_u64(time.wall_nanos()) }
}
func main() -> i32 {
    t := Tok { id: 1u64 }
    return 0i32
}
`, []string{msg("time.wall_nanos()", "time.wall_nanos", "kern_time_wall_ns")}},
		{"dead", `module m
import "std/time"
func main() -> i32 {
    if false { print_u64(time.wall_nanos()) }
    return 0i32
}
`, []string{msg("time.wall_nanos()", "time.wall_nanos", "kern_time_wall_ns")}},
		{"instantiations", `module m
import "std/time"
func stamp[T](x: own T) -> u64 { return time.wall_nanos() }
func main() -> i32 {
    print_u64(stamp(1u64) + stamp(2u8))
    return 0i32
}
`, []string{msg("time.wall_nanos()", "time.wall_nanos", "kern_time_wall_ns")}},
	} {
		for _, level := range []string{"debug", "release"} {
			if _, got := hosted(t, tc.src, level); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s %s:\n got %q\nwant %q", tc.name, level, got, tc.want)
			}
		}
	}
}

// Library reachability is a fixed point: two library functions that call
// each other, one of which requests a hosted entry, both reach it, and a
// call of the other is reported. The cycle is made by marking two of the
// program's functions as library ones.
func TestHostedOnlyLibraryCycle(t *testing.T) {
	src := `module m
import "std/io"
func ping(n: u64) -> u64 { if n == 0u64 { return u64(io.io_argc()) } return pong(n - 1u64) }
func pong(n: u64) -> u64 { return ping(n) }
func main() -> i32 {
    print_u64(pong(3u64))
    return 0i32
}
`
	mod := buildBudgeted(t, map[string]string{"m.kern": src}, "debug").mod
	for _, name := range []string{"m.ping", "m.pong"} {
		mod.Func(name).Library = true
	}
	res, got := hostedMod(t, src, mod)
	if want := []string{msg("pong(3u64)", "m.pong", "kern_io_argc")}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, name := range []string{"m.ping", "m.pong"} {
		if !reflect.DeepEqual(res.Library[name], []string{"kern_io_argc"}) {
			t.Errorf("Library[%s] = %v", name, res.Library[name])
		}
	}
}

// A call the compiler generated with no position is reported at its
// function's declaration (F1).
func TestHostedOnlyNoPosition(t *testing.T) {
	src := `module m
import "std/time"
func main() -> i32 {
    print_u64(time.wall_nanos())
    return 0i32
}
`
	mod := buildBudgeted(t, map[string]string{"m.kern": src}, "debug").mod
	main := mod.Func("m.main")
	for _, b := range main.Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ir.Call); ok && strings.HasPrefix(c.Callee, "time.wall_nanos") {
				c.Pos = ast.Pos{}
			}
		}
	}
	_, got := hostedMod(t, src, mod)
	if len(got) != 1 || !strings.HasPrefix(got[0], "func main()") {
		t.Errorf("got %q, want one diagnostic at main's declaration", got)
	}
}

// The timeout functions of specs/std-timeouts reach the hosted runtime, so
// a freestanding call to each is ENV_HOSTED_ONLY at the call (R6).
func TestHostedOnlyTimeouts(t *testing.T) {
	src := `module m
import "std/io"
import "std/process"
import "std/time"
func main() -> i32 {
    var buf: [4]u8 = zero()
    r := io.read_stdin_timeout(buf, time.millis(1u64))
    c := process.Command.new("true")
    with o := c.output_timeout("", time.millis(1u64)) { }
    with s := c.spawn() {
        match s {
            Ok(ch) => {
                w := ch.wait_timeout(time.millis(1u64))
                with p := ch.take_stdout() {
                    match p {
                        Ok(pipe) => { x := pipe.read_timeout(buf, time.millis(1u64)) }
                        Err(e) => { }
                    }
                }
            }
            Err(e) => { }
        }
    }
    return 0i32
}
`
	for _, level := range []string{"debug", "release"} {
		_, got := hosted(t, src, level)
		joined := strings.Join(got, "\n")
		for _, call := range []string{"io.read_stdin_timeout(", "c.output_timeout(", "ch.wait_timeout(", "pipe.read_timeout("} {
			if !strings.Contains(joined, call) {
				t.Errorf("%s: no ENV_HOSTED_ONLY at %s…\n%s", level, call, joined)
			}
		}
	}
}

// The runtime objects' try_new forms need the hosted runtime, as their new
// forms do (specs/fallible-runtime-objects R5).
func TestHostedOnlyRuntimeTryNew(t *testing.T) {
	src := `module m
func main() -> i32 {
    s := Shared.try_new(1u64)
    m := Mutex.try_new(2u64)
    c := Condvar.try_new()
    return 0i32
}
`
	for _, level := range []string{"debug", "release"} {
		_, got := hosted(t, src, level)
		joined := strings.Join(got, "\n")
		for _, call := range []string{"Shared.try_new(1u64)", "Mutex.try_new(2u64)", "Condvar.try_new()"} {
			if !strings.Contains(joined, call) {
				t.Errorf("%s: no ENV_HOSTED_ONLY at %s\n%s", level, call, joined)
			}
		}
	}
}
