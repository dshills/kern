package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ast"
)

const outlineSrc = `module app
const Limit: u64 = 10u64
thread_local Count: u64 = 0u64
struct Point { x: u64; y: u64 }
union Shape { Dot; Box(w: u64, h: u64) }
interface Area {
    func area(view self) -> u64
}
struct Token { id: u64 }
impl Token {
    func id_of(view self) -> u64 { return self.id }
    destructor(edit self) { }
}
impl Area for Point {
    func area(view self) -> u64 { return self.x * self.y }
}
func scale(p: view[Point], k: u64) -> u64 { return p.x * k }
func pair(a: u64, b: u64) -> u64 { return a + b }
func main() -> i32 {
    p := Point { x: 2u64, y: 3u64 }
    t := Token { id: 1u64 }
    s := pair(scale(p, 2u64), p.area())
    f := pair
    g := f(1u64, 2u64)
    return 0i32
}
`

func (c *client) symbols(uri string) []DocumentSymbol {
	c.t.Helper()
	var out []DocumentSymbol
	if e := c.call("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": uri}}, &out); e != nil {
		c.t.Fatalf("documentSymbol: %+v", e)
	}
	return out
}

// TestDocumentSymbols (specs/editor-outline R1): every kind of top-level
// declaration, with its children, details and name selection, in source
// order; a document that does not parse has none.
func TestDocumentSymbols(t *testing.T) {
	dir := writeTree(t, map[string]string{"main.kern": outlineSrc})
	uri := URIOf(filepath.Join(dir, "main.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(uri, outlineSrc)
	got := c.symbols(uri)
	type want struct {
		name     string
		kind     int
		detail   string
		children string // name:kind:detail, ";"-joined
	}
	wants := []want{
		{"Limit", skConstant, "u64", ""},
		{"Count", skVariable, "u64", ""},
		{"Point", skStructKind, "", "x:8:u64;y:8:u64"},
		{"Shape", skEnum, "", "Dot:22:;Box:22:u64, u64"},
		{"Area", skInterface, "", "area:6:func(Self) -> u64"},
		{"Token", skStructKind, "", "id:8:u64"},
		{"impl Token", skObject, "", "id_of:6:func(Token) -> u64;destructor:6:"},
		{"impl Area for Point", skObject, "", "area:6:func(Point) -> u64"},
		{"scale", skFunction, "func(view[Point], u64) -> u64", ""},
		{"pair", skFunction, "func(u64, u64) -> u64", ""},
		{"main", skFunction, "func() -> i32", ""},
	}
	if len(got) != len(wants) {
		var names []string
		for _, g := range got {
			names = append(names, g.Name)
		}
		t.Fatalf("symbols %v, want %d", names, len(wants))
	}
	for i, w := range wants {
		g := got[i]
		var kids []string
		for _, k := range g.Children {
			kids = append(kids, k.Name+":"+itoa(k.Kind)+":"+k.Detail)
		}
		if g.Name != w.name || g.Kind != w.kind || g.Detail != w.detail || strings.Join(kids, ";") != w.children {
			t.Errorf("symbol %d = %s kind %d detail %q children %q; want %+v", i, g.Name, g.Kind, g.Detail, strings.Join(kids, ";"), w)
		}
	}
	// the selection is the name; the range the whole declaration
	scale := got[8]
	if scale.SelectionRange.Start != pos(outlineSrc, "scale(", 0, 0) || scale.SelectionRange.End != pos(outlineSrc, "scale(", 0, 5) {
		t.Errorf("scale's selection %+v", scale.SelectionRange)
	}
	if scale.Range.Start != pos(outlineSrc, "func scale", 0, 0) {
		t.Errorf("scale's range %+v", scale.Range)
	}
	// a struct whose name also occurs in a field type: the name selected
	// is the declaration's own
	if box := got[3].Children[1]; box.SelectionRange.Start != pos(outlineSrc, "Box(", 0, 0) {
		t.Errorf("Box's selection %+v", box.SelectionRange)
	}
	// a contract and a comment that mention the name come first
	contracted := "module app\ncontract { requires \"pair is positive\" }\nfunc pair(a: u64) -> u64 { // pair\n    return a\n}\n"
	if sel := nameIn([]byte(contracted), ast.Pos{Start: 11, End: len(contracted)}, "pair"); string(contracted[sel.Start:sel.End]) != "pair" || sel.Start != strings.Index(contracted, "pair(") {
		t.Errorf("nameIn selected %d..%d, want the declaration's pair at %d", sel.Start, sel.End, strings.Index(contracted, "pair("))
	}
	if impl := got[7]; impl.SelectionRange.Start != pos(outlineSrc, "Point {\n    func area", 0, 0) {
		t.Errorf("impl Area for Point's selection %+v (the target)", impl.SelectionRange)
	}
	c.change(uri, 2, "module app\nfunc broken( {\n")
	if got := c.symbols(uri); len(got) != 0 {
		t.Errorf("a document that does not parse: %d symbols", len(got))
	}
}

func (c *client) signature(uri string, p Position) *SignatureHelp {
	c.t.Helper()
	var raw json.RawMessage
	if e := c.call("textDocument/signatureHelp", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}, &raw); e != nil {
		c.t.Fatalf("signatureHelp: %+v", e)
	}
	if string(raw) == "null" || len(raw) == 0 {
		return nil
	}
	var h SignatureHelp
	if err := json.Unmarshal(raw, &h); err != nil {
		c.t.Fatal(err)
	}
	return &h
}

// TestSignatureHelp (specs/editor-outline R2): the active parameter of a
// plain call, a method call without self, the inner of two nested calls,
// an unclosed call being typed, commas inside nested calls and strings
// not counted, and null outside a call and through a function value.
func TestSignatureHelp(t *testing.T) {
	dir := writeTree(t, map[string]string{"main.kern": outlineSrc})
	uri := URIOf(filepath.Join(dir, "main.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(uri, outlineSrc)
	check := func(what string, h *SignatureHelp, label string, active int, params ...string) {
		t.Helper()
		if h == nil {
			t.Fatalf("%s: no signature", what)
		}
		sig := h.Signatures[0]
		var got []string
		for _, p := range sig.Parameters {
			got = append(got, sig.Label[p.Label[0]:p.Label[1]])
		}
		if sig.Label != label || h.ActiveParameter != active || strings.Join(got, "|") != strings.Join(params, "|") {
			t.Errorf("%s: %q active %d params %q; want %q active %d params %q", what, sig.Label, h.ActiveParameter, got, label, active, params)
		}
	}
	pairSig := "pair(a: u64, b: u64) -> u64"
	scaleSig := "scale(p: view[Point], k: u64) -> u64"
	// pair(scale(p, 2u64), p.area()): at pair's first argument, before scale
	check("first argument", c.signature(uri, pos(outlineSrc, "pair(scale", 0, 5)), pairSig, 0, "a: u64", "b: u64")
	check("second argument", c.signature(uri, pos(outlineSrc, " p.area())", 0, 0)), pairSig, 1, "a: u64", "b: u64")
	check("inner call", c.signature(uri, pos(outlineSrc, "2u64), p.area", 0, 0)), scaleSig, 1, "p: view[Point]", "k: u64")
	check("method call", c.signature(uri, pos(outlineSrc, "area())", 0, 5)), "area() -> u64", 0)
	if h := c.signature(uri, pos(outlineSrc, "p := Point", 0, 0)); h != nil {
		t.Errorf("outside a call: %+v", h)
	}
	if h := c.signature(uri, pos(outlineSrc, "f(1u64", 0, 2)); h != nil {
		t.Errorf("a call through a function value: %+v", h)
	}
	// an unclosed call being typed, with a comma inside a nested call and
	// one inside a string
	typed := strings.Replace(outlineSrc, "    return 0i32\n}", "    q := pair(scale(p, 1u64), \"a,b\".len(), \n    return 0i32\n}", 1)
	c.change(uri, 2, typed)
	check("unclosed call", c.signature(uri, pos(typed, "len(), \n", 0, 7)), pairSig, 1, "a: u64", "b: u64")
	// nested unclosed calls: the inner one, its first argument
	nested := strings.Replace(outlineSrc, "    return 0i32\n}", "    q := pair(scale(\n    return 0i32\n}", 1)
	c.change(uri, 3, nested)
	check("nested unclosed calls", c.signature(uri, pos(nested, "pair(scale(\n", 0, 11)), scaleSig, 0, "p: view[Point]", "k: u64")
	// whitespace between the callee and its arguments (a comment there
	// would end the line, and with it the call)
	spaced := strings.Replace(outlineSrc, "    return 0i32\n}", "    r := pair  (1u64, 2u64)\n    return 0i32\n}", 1)
	c.change(uri, 4, spaced)
	check("space before the arguments", c.signature(uri, pos(spaced, " 2u64)\n    return", 0, 1)), pairSig, 1, "a: u64", "b: u64")
	if got := openClosers([]byte("func f() {\n    x := a(b[c(\"(\" // (\n")); got != ")])" {
		t.Errorf("openClosers = %q, want %q", got, ")])")
	}
	if n := topLevelCommas([]byte(`scale(p, 1u64), "a,b".len(), 'x', // c, d` + "\n" + `y`)); n != 3 {
		t.Errorf("topLevelCommas = %d, want 3", n)
	}
}
