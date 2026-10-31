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
