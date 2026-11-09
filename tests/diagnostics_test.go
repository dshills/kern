package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// The trap record's line and column and the backtrace (specs/runtime area
// E), in the interpreter and natively at debug and release.

// trapLine is the default handler's record.
type trapLine struct {
	Trap struct {
		Kind string `json:"kind"`
		File string `json:"file"`
		Span struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"span"`
		Line   int    `json:"line"`
		Col    int    `json:"col"`
		Thread uint64 `json:"thread"`
	} `json:"trap"`
}

// backtraceRecord is the line that follows it when backtraces are on.
type backtraceRecord struct {
	Backtrace struct {
		Thread uint64 `json:"thread"`
		Frames []struct {
			Function string `json:"function"`
			File     string `json:"file"`
			Line     int    `json:"line"`
		} `json:"frames"`
		Truncated bool `json:"truncated"`
	} `json:"backtrace"`
}

// interpTrap runs files in the interpreter at an optimization level and
// returns its standard error.
func interpTrap(t *testing.T, files []*source.File, level string, backtrace bool) string {
	t.Helper()
	prog := resolve.ResolveFor(files, level == "release")
	if ds := check.Check(prog); len(prog.Diags)+len(ds) > 0 {
		t.Fatalf("check: %v %v", prog.Diags, ds)
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if err := opt.Run(mod, opt.OptionsFor(level)); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	it := interp.New(mod, files[0].Name, &out, &errb)
	it.SetBacktrace(backtrace)
	if status := it.Run(); status != 134 {
		t.Fatalf("the interpreter did not trap (status %d, stderr %q)", status, errb.String())
	}
	return errb.String()
}

// nativeTrap builds files natively and returns the standard error of the
// run, which must end by SIGABRT.
func nativeTrap(t *testing.T, files []*source.File, opts native.Options, env ...string) string {
	t.Helper()
	if !native.Available() {
		t.Skip("clang not available")
	}
	exe := filepath.Join(t.TempDir(), "prog")
	if ds, err := native.Build(files, exe, opts); err != nil || len(ds) > 0 {
		t.Fatalf("native build: %v %s", err, render(ds))
	}
	cmd := exec.Command(exe)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	if err := cmd.Run(); err == nil {
		t.Fatalf("the program did not trap; stderr %q", eb.String())
	}
	return eb.String()
}

func parseTrap(t *testing.T, stderr string) trapLine {
	t.Helper()
	var rec trapLine
	line, _, _ := strings.Cut(stderr, "\n")
	if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Trap.Kind == "" {
		t.Fatalf("no trap record in %q", stderr)
	}
	return rec
}

// lineColOf is the 1-based line and byte column of offset at in src, by
// counting: independent of the runtimes' binary search.
func lineColOf(src string, at int) (int, int) {
	line, col := 1, 1
	for i := 0; i < at; i++ {
		if src[i] == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}

// RE1 and RE3: the record's line and column name the first byte of the
// trap's span, identically in the three engines.
func TestTrapLineAndColumn(t *testing.T) {
	cases := []struct {
		name string
		kind string
		file string // the file the record names
		at   string // the text the span starts at, in that file
		srcs map[string]string
	}{
		{"an index on the first line of code", "bounds", "t.kern", "xs[i]", map[string]string{"t.kern": "module t\nfunc main() -> i32 { var xs: [2]u64 = [1u64, 2u64]; i := 5usize; print_u64(xs[i]); return 0i32 }\n"}},
		{"the last line of a file with no final newline", "bounds", "t.kern", "xs[i]", map[string]string{"t.kern": "module t\nfunc get(xs: view[]u64, i: usize) -> u64 {\n    return xs[i]\n}\nfunc main() -> i32 { var xs: [2]u64 = [1u64, 2u64]; print_u64(get(xs, 7usize)); return 0i32 }"}},
		{"after a multi-byte character on the line", "overflow", "t.kern", "a + b", map[string]string{"t.kern": "module t\nfunc add(a: u8, b: u8) -> u8 {\n    print_str(\"é→\"); return a + b\n}\nfunc main() -> i32 { print_u64(u64(add(200u8, 100u8))); return 0i32 }\n"}},
		{"a trap the library raises names the user's call", "bounds", "t.kern", "v.get(9usize)", map[string]string{"t.kern": "module t\nfunc main() -> i32 {\n    v := Vec[u64].new()\n    v.push(1u64)\n\n    print_u64(v.get(9usize))\n    return 0i32\n}\n"}},
		{"an explicit trap", "explicit", "t.kern", "trap_kind(explicit)", map[string]string{"t.kern": "module t\n\n\n\nfunc main() -> i32 {\n\t\ttrap_kind(explicit)\n    return 0i32\n}\n"}},
		{"a trap the runtime raises names the call", "explicit", "t.kern", "s2.get().lock()", map[string]string{"t.kern": "module t\nfunc main() -> i32 {\n    s := Shared[Mutex[u64]].new(Mutex[u64].new(1u64))\n    s2 := s.clone()\n    with a := s.get().lock() {\n        with b := s2.get().lock() { b = 2u64 }\n    }\n    return 0i32\n}\n"}},
		{"a trap in the program's second file", "div_zero", "b.kern", "a / b", map[string]string{"a.kern": "module t\nfunc main() -> i32 { print_u64(div(1u64, 0u64)); return 0i32 }\n", "b.kern": "module t\n// a comment line\nfunc div(a: u64, b: u64) -> u64 {\n    return a / b\n}\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var files []*source.File
			for _, name := range []string{"t.kern", "a.kern", "b.kern"} {
				if src, ok := c.srcs[name]; ok {
					files = append(files, &source.File{Name: name, Bytes: []byte(src)})
				}
			}
			src := c.srcs[c.file]
			at := strings.LastIndex(src, c.at)
			if at < 0 {
				t.Fatalf("%q is not in %s", c.at, c.file)
			}
			wantLine, wantCol := lineColOf(src, at)
			records := map[string]string{
				"interpreter, debug":   interpTrap(t, files, "debug", false),
				"interpreter, release": interpTrap(t, files, "release", false),
				"native, debug":        nativeTrap(t, files, native.Options{Opt: "debug"}),
				"native, release":      nativeTrap(t, files, native.Options{Opt: "release"}),
			}
			first := records["interpreter, debug"]
			for engine, stderr := range records {
				rec := parseTrap(t, stderr)
				if rec.Trap.Kind != c.kind || rec.Trap.File != c.file || rec.Trap.Span.Start != at || rec.Trap.Line != wantLine || rec.Trap.Col != wantCol {
					t.Errorf("%s: %s, want kind %s in %s at offset %d, line %d, column %d", engine, strings.TrimSpace(stderr), c.kind, c.file, at, wantLine, wantCol)
				}
				if strings.Count(stderr, "\n") != 1 {
					t.Errorf("%s: the handler wrote more than the record: %q", engine, stderr)
				}
				if stderr != first {
					t.Errorf("%s: the record differs from the interpreter's:\n%s%s", engine, stderr, first)
				}
			}
		})
	}
}

// RE1: the line table's own rules, which the three runtimes implement
// alike: the first byte of a line, the position after the last byte, an
// empty file, an offset past the end, a file with no table and the span
// 0-0 that stands for "no location".
func TestLineColRules(t *testing.T) {
	mod := &ir.Module{Files: []ir.SourceFile{
		{Name: "a.kern", LineStarts: ir.LineStartsOf([]byte("ab\ncd\n")), Size: 6},
		{Name: "b.kern", LineStarts: ir.LineStartsOf([]byte("ab\ncd")), Size: 5},
		{Name: "empty.kern", LineStarts: ir.LineStartsOf(nil), Size: 0},
	}}
	cases := []struct {
		file       string
		start, end int
		line, col  int
	}{
		{"a.kern", 0, 1, 1, 1}, {"a.kern", 1, 2, 1, 2}, {"a.kern", 2, 3, 1, 3}, // the newline belongs to its line
		{"a.kern", 3, 4, 2, 1}, {"a.kern", 5, 6, 2, 3},
		{"a.kern", 6, 6, 3, 1}, // the position after the last byte: the empty last line
		{"a.kern", 7, 8, 0, 0}, // past the end
		{"b.kern", 4, 5, 2, 2}, {"b.kern", 5, 5, 2, 3}, {"b.kern", 6, 7, 0, 0},
		{"empty.kern", 0, 1, 1, 1}, {"empty.kern", 1, 2, 0, 0},
		{"a.kern", 0, 0, 0, 0},       // no location
		{"missing.kern", 3, 4, 0, 0}, // no table
		{"a.kern", -1, 2, 0, 0},
	}
	for _, c := range cases {
		if line, col := mod.LineCol(c.file, c.start, c.end); line != c.line || col != c.col {
			t.Errorf("%s %d-%d: line %d column %d, want %d %d", c.file, c.start, c.end, line, col, c.line, c.col)
		}
	}
}

const chainProgram = `module t
func third(xs: view[]u64, i: usize) -> u64 {
    return xs[i]
}
func second(xs: view[]u64, i: usize) -> u64 { return third(xs, i) + 1u64 }
func first(i: usize) -> u64 {
    var xs: [3]u64 = [1u64, 2u64, 3u64]
    return second(xs, i)
}
func main() -> i32 {
    print_u64(first(7usize))
    return 0i32
}
`

const deepChainProgram = `module t
func down(n: u64, xs: view[]u64) -> u64 {
    if n == 0u64 { return xs[len(xs)] }
    return down(n - 1u64, xs) + 1u64
}
func on_thread() -> u64 {
    var xs: [2]u64 = [1u64, 2u64]
    return down(100u64, xs)
}
func run() -> Result[u64, SpawnErr] {
    t := thread.spawn(on_thread)?
    return Ok(t.join())
}
func main() -> i32 {
    r := run()
    match r {
        Ok(v) => print_u64(v)
        Err(e) => print_str("spawn failed")
    }
    return 0i32
}
`

func parseBacktrace(t *testing.T, stderr string) (backtraceRecord, string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want the trap record and one backtrace line, got %q", stderr)
	}
	var rec backtraceRecord
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil || rec.Backtrace.Frames == nil {
		t.Fatalf("the second line is not a backtrace: %q", lines[1])
	}
	return rec, lines[1]
}

func functions(rec backtraceRecord) string {
	var names []string
	for _, f := range rec.Backtrace.Frames {
		names = append(names, f.Function)
	}
	return strings.Join(names, " ")
}

// RE2: the backtrace.
func TestBacktrace(t *testing.T) {
	chain := []*source.File{{Name: "t.kern", Bytes: []byte(chainProgram)}}
	t.Run("off by default", func(t *testing.T) {
		for _, stderr := range []string{nativeTrap(t, chain, native.Options{}), interpTrap(t, chain, "debug", false)} {
			if strings.Count(stderr, "\n") != 1 || strings.Contains(stderr, "backtrace") {
				t.Errorf("a backtrace was written without being asked for: %q", stderr)
			}
		}
	})
	t.Run("a debug build with --backtrace lists the calls as written", func(t *testing.T) {
		rec, nativeLine := parseBacktrace(t, nativeTrap(t, chain, native.Options{Backtrace: true}))
		if got := functions(rec); got != "t.third t.second t.first t.main" {
			t.Errorf("frames %q", got)
		}
		wantLines := []int{2, 5, 6, 10}
		for i, f := range rec.Backtrace.Frames {
			if i < len(wantLines) && (f.Line != wantLines[i] || f.File != "t.kern") {
				t.Errorf("frame %d: %+v, want line %d of t.kern", i, f, wantLines[i])
			}
		}
		if rec.Backtrace.Thread != 0 || rec.Backtrace.Truncated {
			t.Errorf("thread %d truncated %v", rec.Backtrace.Thread, rec.Backtrace.Truncated)
		}
		// the interpreter writes the same line from its own frames
		if _, interpLine := parseBacktrace(t, interpTrap(t, chain, "debug", true)); interpLine != nativeLine {
			t.Errorf("the interpreter's line differs:\n%s\n%s", interpLine, nativeLine)
		}
	})
	t.Run("the environment turns it on and off", func(t *testing.T) {
		rec, _ := parseBacktrace(t, nativeTrap(t, chain, native.Options{}, "KERN_BACKTRACE=1"))
		if got := functions(rec); !strings.HasSuffix(got, "t.main") {
			t.Errorf("KERN_BACKTRACE=1 on a plain build: frames %q, want them to end in t.main", got)
		}
		if stderr := nativeTrap(t, chain, native.Options{Backtrace: true}, "KERN_BACKTRACE=0"); strings.Count(stderr, "\n") != 1 {
			t.Errorf("KERN_BACKTRACE=0 did not turn a --backtrace build's backtrace off: %q", stderr)
		}
	})
	t.Run("a release build lists the functions that still have frames", func(t *testing.T) {
		rec, _ := parseBacktrace(t, nativeTrap(t, chain, native.Options{Opt: "release", Backtrace: true}))
		got := functions(rec)
		if !strings.HasSuffix(got, "t.main") || len(rec.Backtrace.Frames) > 4 {
			t.Errorf("frames %q, want at most the four functions, ending in t.main", got)
		}
		for _, f := range rec.Backtrace.Frames {
			if !strings.HasPrefix(f.Function, "t.") || f.File != "t.kern" || f.Line == 0 {
				t.Errorf("frame %+v is not a function of the program", f)
			}
		}
	})
	deep := []*source.File{{Name: "t.kern", Bytes: []byte(deepChainProgram)}}
	t.Run("64 frames, then truncated, on the trapping thread", func(t *testing.T) {
		nat, natLine := parseBacktrace(t, nativeTrap(t, deep, native.Options{Backtrace: true}))
		itp, itpLine := parseBacktrace(t, interpTrap(t, deep, "debug", true))
		for engine, rec := range map[string]backtraceRecord{"native": nat, "interpreter": itp} {
			if len(rec.Backtrace.Frames) != 64 || !rec.Backtrace.Truncated || rec.Backtrace.Thread == 0 {
				t.Errorf("%s: %d frames, truncated %v, thread %d; want 64, true and the spawned thread", engine, len(rec.Backtrace.Frames), rec.Backtrace.Truncated, rec.Backtrace.Thread)
			}
			for _, f := range rec.Backtrace.Frames {
				if f.Function != "t.down" || f.Line != 2 {
					t.Errorf("%s: frame %+v, want t.down at line 2", engine, f)
					break
				}
			}
		}
		if natLine != itpLine {
			t.Errorf("the lines differ:\n%s\n%s", natLine, itpLine)
		}
	})
	t.Run("a stack overflow, found through the fault's registers", func(t *testing.T) {
		const overflow = "module t\nfunc deep(n: u64) -> u64 {\n    var pad: [64]u64 = zero()\n    pad[usize(n % 64u64)] = n\n    return deep(n + 1u64) + pad[usize((n + 1u64) % 64u64)]\n}\nfunc main() -> i32 { print_u64(deep(0u64)); return 0i32 }\n"
		stderr := nativeTrap(t, []*source.File{{Name: "t.kern", Bytes: []byte(overflow)}}, native.Options{Backtrace: true})
		if parseTrap(t, stderr).Trap.Kind != "stack" {
			t.Fatalf("want a stack trap: %q", stderr)
		}
		rec, _ := parseBacktrace(t, stderr)
		if len(rec.Backtrace.Frames) != 64 || !rec.Backtrace.Truncated || rec.Backtrace.Frames[0].Function != "t.deep" {
			t.Errorf("%d frames, truncated %v, first %+v; want 64 of t.deep, truncated", len(rec.Backtrace.Frames), rec.Backtrace.Truncated, rec.Backtrace.Frames[0])
		}
	})
	t.Run("a custom handler gets no backtrace", func(t *testing.T) {
		const handled = "module t\nfunc quiet(info: view[TrapInfo]) { print_str(\"handled\"); println() }\nfunc main() -> i32 {\n    old := trap.set_handler(quiet)\n    var xs: [2]u64 = [1u64, 2u64]\n    i := 5usize\n    print_u64(xs[i])\n    return 0i32\n}\n"
		if stderr := nativeTrap(t, []*source.File{{Name: "t.kern", Bytes: []byte(handled)}}, native.Options{Backtrace: true}); stderr != "" {
			t.Errorf("a custom handler's trap wrote %q", stderr)
		}
	})
}
