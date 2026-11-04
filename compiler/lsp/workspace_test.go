package lsp

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// initializeRoot initializes with a workspace root, as a client with a
// folder open does.
func (c *client) initializeRoot(root string) {
	c.t.Helper()
	var res map[string]any
	params := map[string]any{"rootUri": URIOf(root), "capabilities": map[string]any{}}
	if e := c.call("initialize", params, &res); e != nil {
		c.t.Fatalf("initialize: %+v", e)
	}
	c.notify("initialized", map[string]any{})
}

const (
	wsLib = `module lib
func Make(x: u64) -> u64 { return x + Helper() }
func Helper() -> u64 { return 1u64 }
`
	wsA = `module main
import "lib"
func main() -> i32 {
    a := lib.Make(1u64)
    b := lib.Make(a)
    print_u64(b)
    return 0i32
}
`
	wsB = `module main
import "lib"
func main() -> i32 {
    print_u64(lib.Make(2u64))
    return 0i32
}
`
	wsC = `module main
func Make() -> u64 { return 3u64 }
func main() -> i32 {
    print_u64(Make())
    return 0i32
}
`
)

// renameEdits renames at p and returns, per file name relative to dir,
// the number of edits; e is the error, if any.
func renameEdits(c *client, dir, uri string, p Position, name string) (map[string]int, *rpcError) {
	var we WorkspaceEdit
	e := c.call("textDocument/rename", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p, "newName": name}, &we)
	out := map[string]int{}
	for u, es := range we.Changes {
		path, _ := PathOf(u)
		rel, _ := filepath.Rel(dir, path)
		for _, te := range es {
			if te.NewText != name {
				c.t.Errorf("edit in %s writes %q", rel, te.NewText)
			}
		}
		out[rel] = len(es)
	}
	return out, e
}

func editsString(m map[string]int) string {
	var parts []string
	for f, n := range m {
		parts = append(parts, f+":"+string(rune('0'+n)))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

// With a workspace root, renaming a library function edits every program
// that loads the library, from either side; a program that does not load
// it, or does not load at all, is untouched; a collision in the library
// refuses the rename for all; without a root only the document's program
// is renamed (specs/editor-workspace R1-R4).
func TestRenameWorkspace(t *testing.T) {
	files := map[string]string{
		"kern.mod":        "",
		"lib/lib.kern":    wsLib,
		"a/main.kern":     wsA,
		"b/main.kern":     wsB,
		"c/main.kern":     wsC,
		"d/main.kern":     "module main\nimport \"missing\"\nfunc main() -> i32 { return 0i32 }\n",
		"testdata/x.kern": wsB,
	}
	dir := writeTree(t, files)
	aURI, libURI := URIOf(filepath.Join(dir, "a", "main.kern")), URIOf(filepath.Join(dir, "lib", "lib.kern"))
	want := "a/main.kern:2 b/main.kern:1 lib/lib.kern:1"

	c := start(t, Options{})
	c.initializeRoot(dir)
	c.open(aURI, wsA)
	got, e := renameEdits(c, dir, aURI, pos(wsA, "lib.Make(1u64)", 0, 4), "Build")
	if e != nil {
		t.Fatalf("rename from a: %+v", e)
	}
	if s := editsString(got); s != want {
		t.Errorf("rename from a: %s, want %s", s, want)
	}
	c.open(libURI, wsLib)
	got, e = renameEdits(c, dir, libURI, pos(wsLib, "func Make", 0, 5), "Build")
	if e != nil {
		t.Fatalf("rename from lib: %+v", e)
	}
	if s := editsString(got); s != want {
		t.Errorf("rename from lib: %s, want %s", s, want)
	}
	// a collision in the shared module refuses everywhere
	got, e = renameEdits(c, dir, aURI, pos(wsA, "lib.Make(1u64)", 0, 4), "Helper")
	if e == nil || len(got) != 0 {
		t.Errorf("rename to Helper: edits %v, error %+v; want a refusal and no edits", got, e)
	}

	// no root: the document's program only
	c2 := start(t, Options{})
	c2.initialize(nil, true)
	c2.open(aURI, wsA)
	got, e = renameEdits(c2, dir, aURI, pos(wsA, "lib.Make(1u64)", 0, 4), "Build")
	if e != nil {
		t.Fatalf("rename without a root: %+v", e)
	}
	if s, w := editsString(got), "a/main.kern:2 lib/lib.kern:1"; s != w {
		t.Errorf("rename without a root: %s, want %s", s, w)
	}
}

// Completion works at each of several unfinished dots in one document;
// a range's `..` and a dot in a string or comment are not unfinished
// (specs/editor-workspace R5).
func TestCompletionSeveralDots(t *testing.T) {
	doc := strings.Replace(completeMain, "PROBE", "p.\n    lib.  // trailing spaces and a comment\n    q := 0usize..2usize\n    s := \"x.\"\n    // y.\n    p.", 1)
	dir := writeTree(t, map[string]string{"main.kern": doc, "lib/lib.kern": completeLib})
	uri := URIOf(filepath.Join(dir, "main.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(uri, doc)
	has := func(what string, got, want, not []string) {
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
				t.Errorf("%s: unexpected %s in %v", what, n, got)
			}
		}
	}
	first := c.complete(uri, pos(doc, "p.\n    lib.", 0, 2))
	has("first dot", first, []string{"Sum", "X", "Y"}, []string{"Make", "Limit"})
	second := c.complete(uri, pos(doc, "lib.  //", 0, 4))
	has("second dot", second, []string{"Make", "Limit", "Point"}, []string{"Sum", "hidden"})
	last := c.complete(uri, pos(doc, "    p.\n    later", 0, 6))
	has("last dot", last, []string{"Sum", "X", "Y"}, []string{"Make"})
}
