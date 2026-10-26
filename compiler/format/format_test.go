package format

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// checkCodes resolves and checks files as tests/harness_test.go does and
// returns the sorted multiset of diagnostic codes.
func checkCodes(fs []*source.File) []string {
	prog := resolve.Resolve(fs)
	ds := prog.Diags
	if len(ds) == 0 {
		ds = check.Check(prog)
	}
	return codes(ds)
}

func codes(ds []diag.Diagnostic) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, string(d.Code))
	}
	sort.Strings(out)
	return out
}

func corpus(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, root := range []string{filepath.Join("..", "..", "tests"), filepath.Join("..", "..", "examples")} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".kern") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) == 0 {
		t.Fatal("no .kern files found")
	}
	sort.Strings(out)
	return out
}

// headerLines returns the leading `//!` lines of src verbatim.
func headerLines(src []byte) []string {
	var out []string
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "//!") {
			break
		}
		out = append(out, line)
	}
	return out
}

func commentTexts(src []byte) []string {
	_, cs, _ := lexer.LexWithComments(src)
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, strings.TrimRight(c.Text, " \t\r"))
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func checkInvariants(t *testing.T, out []byte) {
	t.Helper()
	if !bytes.HasSuffix(out, []byte("\n")) || bytes.HasSuffix(out, []byte("\n\n")) {
		t.Errorf("output must end with exactly one newline")
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if strings.TrimRight(line, " \t") != line {
			t.Errorf("line %d has trailing whitespace: %q", i+1, line)
		}
		if strings.Contains(line, "\t") {
			t.Errorf("line %d contains a tab: %q", i+1, line)
		}
	}
	if bytes.Contains(out, []byte("\n\n\n")) {
		t.Errorf("output contains consecutive blank lines")
	}
}

// TestCorpus formats every fixture and example and verifies the properties
// required by spec/format.md: idempotence, diagnostic-code preservation,
// verbatim headers and comment preservation.
func TestCorpus(t *testing.T) {
	dirs := map[string][]*source.File{}
	for _, path := range corpus(t) {
		path := path
		f, err := source.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(path, func(t *testing.T) {
			out, ds := Format(f)
			if ds != nil {
				if out != nil {
					t.Fatalf("output returned alongside diagnostics")
				}
				if want := checkCodes([]*source.File{f}); !equal(codes(ds), want) {
					t.Fatalf("parse failure codes %v differ from check codes %v", codes(ds), want)
				}
				return
			}
			checkInvariants(t, out)
			f2 := &source.File{Name: f.Name, Bytes: out}
			out2, ds2 := Format(f2)
			if ds2 != nil {
				t.Fatalf("formatted output does not parse: %v\n%s", ds2[0].Message, out)
			}
			if !bytes.Equal(out, out2) {
				t.Fatalf("not idempotent:\n--- first ---\n%s--- second ---\n%s", out, out2)
			}
			if want, got := checkCodes([]*source.File{f}), checkCodes([]*source.File{f2}); !equal(want, got) {
				t.Fatalf("diagnostic codes changed: %v -> %v\n%s", want, got, out)
			}
			if want, got := headerLines(f.Bytes), headerLines(out); !equal(want, got) {
				t.Fatalf("headers changed: %q -> %q", want, got)
			}
			if want, got := commentTexts(f.Bytes), commentTexts(out); !equal(want, got) {
				t.Fatalf("comments changed: %q -> %q\n%s", want, got, out)
			}
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], f2)
		})
	}
	// Multi-file fixtures are checked as a unit, as the harness does.
	for dir, formatted := range dirs {
		if len(formatted) < 2 || !strings.Contains(dir, string(filepath.Separator)+"negative"+string(filepath.Separator)) &&
			!strings.Contains(dir, string(filepath.Separator)+"positive"+string(filepath.Separator)) {
			continue
		}
		var orig []*source.File
		for _, f := range formatted {
			o, err := source.Load(f.Name)
			if err != nil {
				t.Fatal(err)
			}
			orig = append(orig, o)
		}
		if want, got := checkCodes(orig), checkCodes(formatted); !equal(want, got) {
			t.Errorf("%s: diagnostic codes changed as a unit: %v -> %v", dir, want, got)
		}
	}
}

func TestComments(t *testing.T) {
	src := `//! phase: 1
//! expect: accept
// leading comment
module m


// about Pair
struct Pair { a: u64; b: u64 }   // trailing on a decl

struct Multi {
    // first field
    x: u64   // x
    y: u64
}

contract {
    requires "a > 0"
}
func f(a: u64) -> u64 {   // after brace
    // own line in body
    x := a + 1u64   // trailing
    if x > 2u64 { return x } else { return 0u64 }   // after if

    match Some(x) {
        // before arm
        Some(v) => return v   // arm trailing
        _ => { }
    }
    // last in body
}

func g() -> u64 { return 1u64 }   // one-liner
// end of file
`
	want := `//! phase: 1
//! expect: accept
// leading comment
module m

// about Pair
struct Pair { a: u64; b: u64 }  // trailing on a decl

struct Multi {
    // first field
    x: u64  // x
    y: u64
}

contract {
    requires "a > 0"
}
func f(a: u64) -> u64 {  // after brace
    // own line in body
    x := a + 1u64  // trailing
    if x > 2u64 { return x } else { return 0u64 }  // after if

    match Some(x) {
        // before arm
        Some(v) => return v  // arm trailing
        _ => { }
    }
    // last in body
}

func g() -> u64 { return 1u64 }  // one-liner
// end of file
`
	f := &source.File{Name: "c.kern", Bytes: []byte(src)}
	out, ds := Format(f)
	if ds != nil {
		t.Fatalf("diagnostics: %v", ds)
	}
	if string(out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	out2, _ := Format(&source.File{Name: "c.kern", Bytes: out})
	if !bytes.Equal(out, out2) {
		t.Fatalf("not idempotent:\n%s", out2)
	}
}

// TestLayout covers constructs the corpus may not exercise: every expression
// and declaration form is printed and round-trips.
func TestLayout(t *testing.T) {
	src := `module m
import "core"
view struct V[T: Copy + Send] nocopy { p: RawPtr[T]; s: view[]u8; a: [4]u8; e: edit[T]; f: func(own T, out u8) -> (view[T], u8); u: () }
union U[T] { A; B(T, u8); C(x: T, y: core.Thing[T]) }
interface I[T] { func m(view self) -> T; func n[U](edit self, v: own U) }
impl [T: Copy] I[T] for V[T] {
    func m(view self) -> T { return copy(self.e) }
    destructor(edit self) { x := 1u64; drop(x) }
    contract { invariant "x" }
    func n[U](edit self, v: own U) { }
}
impl V[u8] { func k(own self) release r { } }
extern "C" func c_abs(v: i32) -> i32
const N: usize = 4usize
unsafe assert Send for V[u8]
func all(a: own Box[u64], b: out P, c: view[]u8) -> Result[(), u8] {
    var p: P
    var q: u64 = 1_000u64
    x := -5i64 - -3i64 + !true || (1u64 < 2u64) && 3.5f32 >= 2.0f32
    y := truncate[u8](x) + u8(q) * 2u8 << 1u8 >> 1u8 & 3u8 | 4u8 ^ 5u8 % 2u8 / 1u8
    z := take(p.x)
    w := replace(p.x, move q)
    unsafe { c_abs(-1i32) }
    with l, r := get2(out b, c[1usize:3usize], c[:], c[2usize:], c[:1usize]) { l = r }
    for i in 0usize..N { continue }
    while false { break }
    if x == 1i64 { } else if x == 2i64 { } else { }
    match move a {
        core.Opt.Some(v) => { return Ok(()) }
        Opt.None => return Err(1u8)
        _ => trap()
    }
    s := P { x: 1u64 }
    big := P {
        x: 1u64,
    }
    e := P { }
    f[u64, Box[[]u8], ()](x)?
    { }
    return Ok(())
}
`
	f := &source.File{Name: "l.kern", Bytes: []byte(src)}
	out, ds := Format(f)
	if ds != nil {
		t.Fatalf("diagnostics: %v", ds[0].Message)
	}
	if string(out) != src {
		t.Fatalf("expected the canonical input to be unchanged; got:\n%s", out)
	}
	checkInvariants(t, out)
}

func TestParseFailure(t *testing.T) {
	f := &source.File{Name: "bad.kern", Bytes: []byte("module m\nfunc f( {\n")}
	out, ds := Format(f)
	if out != nil || len(ds) != 1 || ds[0].Code != "SYNTAX" {
		t.Fatalf("got %q, %v", out, ds)
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }
