package lsp

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func (c *client) complete(uri string, p Position) []string {
	c.t.Helper()
	var l CompletionList
	if e := c.call("textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}, &l); e != nil {
		c.t.Fatalf("completion: %+v", e)
	}
	var out []string
	for _, it := range l.Items {
		out = append(out, it.Label)
	}
	sort.Strings(out)
	return out
}

const completeLib = `module lib
const Limit: u64 = 10u64
struct Point { X: u64; Y: u64; hidden: u64 }
impl Point {
    func Sum(view self) -> u64 { return self.X + self.Y }
    func Origin() -> Point { return Point { X: 0u64, Y: 0u64, hidden: 0u64 } }
}
func Make(x: u64) -> Point { return Point { X: x, Y: 2u64, hidden: 1u64 } }
func helper() -> u64 { return 1u64 }
`

const completeMain = `module app
import "lib"
union Shape { Circle; Square }
func main() -> i32 {
    p := lib.Make(3u64)
    total := p.Sum()
    if total > 1u64 {
        inner := 4u64
    }
    PROBE
    later := 5u64
    return 0i32
}
`

// TestCompletion (specs/editor-complete R1-R4): members after a dot, with
// another module's private fields left out; an imported module's exported
// declarations; locals in scope only; the prelude's functions; and the
// variants of a type.
func TestCompletion(t *testing.T) {
	base := strings.Replace(completeMain, "PROBE", "", 1)
	dir := writeTree(t, map[string]string{"main.kern": base, "lib/lib.kern": completeLib})
	uri := URIOf(filepath.Join(dir, "main.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(uri, base)
	version := 1
	// probe completes at the end of probe, written on its own line
	probe := func(text string) []string {
		t.Helper()
		version++
		src := strings.Replace(completeMain, "PROBE", text, 1)
		c.change(uri, version, src)
		return c.complete(uri, pos(src, text+"\n    later", 0, len(text)))
	}
	has := func(what string, got []string, want, not []string) {
		t.Helper()
		set := map[string]bool{}
		for _, g := range got {
			set[g] = true
		}
		for _, w := range want {
			if !set[w] {
				t.Errorf("%s: missing %s in %v", what, w, got)
			}
		}
		for _, n := range not {
			if set[n] {
				t.Errorf("%s: %s should not be offered: %v", what, n, got)
			}
		}
	}
	has("p.", probe("p."), []string{"X", "Y", "Sum"}, []string{"hidden", "Origin", "Make", "main"})
	has("p.S", probe("p.S"), []string{"Sum"}, []string{"X"})
	has("lib.", probe("lib."), []string{"Make", "Point", "Limit"}, []string{"helper", "X"})
	has("lib.Point.", probe("lib.Point."), []string{"Origin"}, []string{"Sum", "X"})
	has("Shape.", probe("Shape."), []string{"Circle", "Square"}, []string{"X"})
	has("to", probe("to"), []string{"total"}, []string{"later", "inner", "p"})
	has("print_", probe("print_"), []string{"print_u64", "print_str"}, []string{"total"})
	// an empty prefix offers locals, keywords, the module's names and its imports
	has("blank", probe(""), []string{"p", "total", "lib", "while", "main", "Shape"}, []string{"later", "inner"})
	if got := c.complete("untitled:1", Position{}); len(got) != 0 {
		t.Errorf("a non-kern document: %v", got)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// completeItems is complete with each label's detail.
func (c *client) completeItems(uri string, p Position) map[string]string {
	c.t.Helper()
	var l CompletionList
	if e := c.call("textDocument/completion", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}, &l); e != nil {
		c.t.Fatalf("completion: %+v", e)
	}
	out := map[string]string{}
	for _, it := range l.Items {
		out[it.Label] = it.Detail
	}
	return out
}

func hasItems(t *testing.T, what string, got map[string]string, want, not []string) {
	t.Helper()
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("%s: missing %s in %v", what, w, got)
		}
	}
	for _, n := range not {
		if _, ok := got[n]; ok {
			t.Errorf("%s: %s should not be offered: %v", what, n, got)
		}
	}
}

// TestCompletionConstraints (specs/editor-complete-gaps R1, R2, R5): the
// methods of a type parameter's interface constraints, the first
// constraint's signature for a shared name, nothing for a marker
// constraint, where-clause bounds, a primitive's methods, and nothing for
// an unresolved receiver.
func TestCompletionConstraints(t *testing.T) {
	src := `module app
import "std/map"
interface Named {
    func hash(view self, seed: u64) -> u64
    func label(view self) -> u64
}
struct Pair[T] { a: T }
impl Pair[T] {
    func h(view self) -> u64 where T: map.Hashable {
        /*self.a.*/
        return 0u64
    }
}
func one[T: map.Hashable](x: view[T]) -> u64 {
    /*x.*/
    return 0u64
}
func two[T: map.Hashable + Named](x: view[T]) -> u64 {
    /*x.*/
    return 0u64
}
func marker[T: Copy](x: T) -> u64 {
    /*x.*/
    return 0u64
}
func main() -> i32 {
    n := 3u64
    /*n.*/
    /*zz.*/
    return i32(n)
}
`
	got := probeEach(t, src)
	hasItems(t, "where-clause", got[0], []string{"hash", "eq"}, []string{"a", "label"})
	hasItems(t, "T: Hashable", got[1], []string{"hash", "eq"}, []string{"label"})
	hasItems(t, "T: Hashable + Named", got[2], []string{"hash", "eq", "label"}, nil)
	if d := got[2]["hash"]; d != got[1]["hash"] || strings.Contains(d, "seed") {
		t.Errorf("a shared name takes the first constraint's signature: %q (Hashable's is %q)", d, got[1]["hash"])
	}
	if len(got[3]) != 0 {
		t.Errorf("a marker constraint offers nothing: %v", got[3])
	}
	hasItems(t, "u64", got[4], []string{"hash", "eq"}, nil)
	if len(got[5]) != 0 {
		t.Errorf("an unresolved receiver offers nothing: %v", got[5])
	}
}

// probeEach probes every /*...*/ marker of src in order, each typed alone.
func probeEach(t *testing.T, src string) []map[string]string {
	t.Helper()
	var out []map[string]string
	base, _ := stripAt(src, -1)
	dir := writeTree(t, map[string]string{"main.kern": base})
	uri := URIOf(filepath.Join(dir, "main.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(uri, base)
	if p := c.publication(uri); len(p.Diagnostics) != 0 {
		t.Fatalf("the probe source does not resolve: %s", codesOf(p.Diagnostics))
	}
	for k := 0; k < strings.Count(src, "/*"); k++ {
		text, off := stripAt(src, k)
		c.change(uri, k+2, text)
		out = append(out, c.completeItems(uri, ToPosition([]byte(text), off, UTF16)))
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
	return out
}

// stripAt replaces the k-th /*text*/ marker of src by its text and removes
// the others, returning the offset after that text (-1 for k < 0).
func stripAt(src string, k int) (string, int) {
	var b strings.Builder
	off := -1
	for n := 0; ; n++ {
		i := strings.Index(src, "/*")
		if i < 0 {
			b.WriteString(src)
			return b.String(), off
		}
		j := strings.Index(src[i:], "*/")
		if j < 0 {
			panic("an unterminated marker in a probe source")
		}
		b.WriteString(src[:i])
		if n == k {
			b.WriteString(src[i+2 : i+j])
			off = b.Len()
		}
		src = src[i+j+2:]
	}
}

// TestCompletionHeaderScope (specs/editor-complete-gaps R3, R5): a for
// variable, a with name and a match arm binding are offered inside their
// bodies, at both braces, and not after them; a local declared before the
// loop is offered in both places.
func TestCompletionHeaderScope(t *testing.T) {
	src := `module app
struct Cell { v: u64 }
impl Cell { func get(view self) -> view[u64] { return self.v } }
func main() -> i32 {
    before := 1u64
    xs := [1u64, 2u64, 3u64]
    total := 0u64
    for loopvar in xs {/*lo*/
        total = total + loopvar
        /*lo*/
        /*be*/
    /*lo*/}
    /*lo*/
    c := Cell { v: 2u64 }
    with withname := c.get() {
        total = total + withname
        /*wi*/
    }
    /*wi*/
    o := Some(4u64)
    match o {
        Some(armvar) => {
            total = total + armvar
            /*ar*/
        }
        None => { }
    }
    /*ar*/
    /**/
    return i32(total + before)
}
`
	got := probeEach(t, src)
	// the loop's probes: after its {, in its body, before its }, after it
	for i, want := range map[int]bool{0: true, 1: true, 3: true, 4: false} {
		if _, ok := got[i]["loopvar"]; ok != want {
			t.Errorf("loopvar at probe %d: offered %v, want %v", i, ok, want)
		}
	}
	hasItems(t, "before, in the loop", got[2], []string{"before"}, nil)
	hasItems(t, "with body", got[5], []string{"withname"}, nil)
	hasItems(t, "after with", got[6], nil, []string{"withname"})
	hasItems(t, "arm body", got[7], []string{"armvar"}, nil)
	hasItems(t, "after match", got[8], nil, []string{"armvar"})
	hasItems(t, "after all", got[9], []string{"before", "total", "c", "o"}, []string{"loopvar", "withname", "armvar"})
}
