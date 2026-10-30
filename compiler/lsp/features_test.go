package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/format"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

const ownProgram = "module app\nfunc total(v: own Vec[u64]) -> u64 {\n    return v.get(0usize)\n}\nfunc main() -> i32 {\n    v := Vec[u64].new()\n    v.push(1u64)\n    n := total(move v)\n    print_u64(n)\n    return 0i32\n}\n"

// at is the position of the n-th occurrence (from 0) of sub in text,
// plus delta bytes, in UTF-16.
func at(text, sub string, n, delta int) Position {
	off := -1
	for i := 0; i <= n; i++ {
		j := strings.Index(text[off+1:], sub)
		if j < 0 {
			panic("no " + sub)
		}
		off += 1 + j
	}
	return ToPosition([]byte(text), off+delta, UTF16)
}

func (c *client) hoverAt(uri string, p Position) (*Hover, *rpcError) {
	c.t.Helper()
	var h *Hover
	e := c.call("textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}, &h)
	return h, e
}

func TestHover(t *testing.T) {
	dir := writeTree(t, map[string]string{"app.kern": ownProgram})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(uri, ownProgram)
	cases := []struct {
		pos  Position
		want string // the whole Markdown value
	}{
		{at(ownProgram, "v.push", 0, 0), "```kern\nlocal v: Vec[u64]\n```\n\nstate: owned\n\n- edit at line 7\n- consume at line 6\n"},
		{at(ownProgram, "v.push", 0, 1), "```kern\nlocal v: Vec[u64]\n```\n\nstate: owned\n\n- edit at line 7\n- consume at line 6\n"}, // the end of the span counts
		{at(ownProgram, "v := ", 0, 0), "```kern\nlocal v: Vec[u64]\n```\n\nstate: owned\n\n- edit at line 7\n- consume at line 6\n"},  // the declaration
		{at(ownProgram, "v: own", 0, 1), "```kern\nparam own v: Vec[u64]\n```\n\nstate: owned\n\n- view at line 3\n\ndropped at line 2\n"},
		{at(ownProgram, "total(move", 0, 2), "```kern\nfunc app.total: func(own Vec[u64]) -> u64\n```\n"},
		{at(ownProgram, "Vec[u64].new", 0, 1), "```kern\ntype Vec\n```\n"},
	}
	for _, k := range cases {
		h, e := c.hoverAt(uri, k.pos)
		if e != nil || h == nil || h.Contents.Kind != "markdown" || h.Contents.Value != k.want {
			t.Errorf("hover at %v: %+v %+v, want %q", k.pos, h, e, k.want)
		}
	}
	if h, e := c.hoverAt(uri, Position{Line: 3, Character: 0}); h != nil || e != nil {
		t.Errorf("hover on a closing brace: %+v %+v", h, e)
	}
	h, _ := c.hoverAt(uri, at(ownProgram, "v.push", 0, 0))
	if h.Range != (Range{Start: Position{6, 4}, End: Position{6, 5}}) {
		t.Errorf("hover range %+v", h.Range)
	}
	// with a diagnostic in the program, only the code block
	broken := ownProgram + "func bad() -> u64 { return nope }\n"
	c.change(uri, 2, broken)
	if h, e := c.hoverAt(uri, at(broken, "v.push", 0, 0)); e != nil || h == nil || h.Contents.Value != "```kern\nlocal v: Vec[u64]\n```\n" {
		t.Errorf("hover with a diagnostic: %+v %+v", h, e)
	}
	// non-kern documents get null
	c.open("untitled:1", ownProgram)
	if h, e := c.hoverAt("untitled:1", Position{6, 4}); h != nil || e != nil {
		t.Errorf("hover on a non-file document: %+v %+v", h, e)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

func TestHoverPanic(t *testing.T) {
	dir := writeTree(t, map[string]string{"app.kern": ownProgram})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	c := start(t, Options{Resolve: func([]*source.File) *resolve.Program { panic("resolver broke") }})
	c.initialize(nil, true)
	c.open(uri, ownProgram)
	if _, e := c.hoverAt(uri, Position{6, 4}); e == nil || e.Code != codeInternalError || !strings.Contains(e.Message, "resolver broke") {
		t.Errorf("hover with a panicking resolver: %+v", e)
	}
	var edits []TextEdit
	if e := c.call("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": uri}}, &edits); e != nil {
		t.Errorf("the next request: %+v", e)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

func applyEdits(text string, edits []TextEdit, enc Encoding) string {
	for i := len(edits) - 1; i >= 0; i-- {
		text = apply([]byte(text), edits[i], enc)
	}
	return text
}

func TestFormatting(t *testing.T) {
	ugly := "module app\nfunc main() -> i32 {\n  return   0i32\n}\n"
	dir := writeTree(t, map[string]string{"app.kern": ugly})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	c := start(t, Options{})
	c.initialize([]string{"utf-8"}, true)
	c.open(uri, ugly)
	format := func() (json.RawMessage, *rpcError) {
		m, _ := c.reply(c.request("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": uri}, "options": map[string]any{"tabSize": 8, "insertSpaces": false}}))
		return m.Result, m.Error
	}
	res, e := format()
	var edits []TextEdit
	if e != nil || json.Unmarshal(res, &edits) != nil || len(edits) != 1 {
		t.Fatalf("formatting: %s %+v", res, e)
	}
	want, ds := formatText(ugly)
	if ds > 0 || applyEdits(ugly, edits, UTF8) != want {
		t.Errorf("formatted %q, want %q", applyEdits(ugly, edits, UTF8), want)
	}
	c.change(uri, 2, want)
	if res, e := format(); e != nil || string(res) != "[]" {
		t.Errorf("formatting canonical text: %s %+v", res, e)
	}
	c.change(uri, 3, "module app\nfunc main( {")
	if res, e := format(); e != nil || string(res) != "null" {
		t.Errorf("formatting unparsable text: %s %+v", res, e)
	}
	if e := c.call("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": "file:///not/open.kern"}}, nil); e == nil || e.Code != codeInvalidParams {
		t.Errorf("formatting a non-document: %+v", e)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

func formatText(text string) (string, int) {
	out, ds := format.Format(&source.File{Name: "x.kern", Bytes: []byte(text)})
	return string(out), len(ds)
}

// TestRangedChange: a ranged change is applied in the negotiated encoding;
// a reversed range ignores the notification with a type-2 log.
func TestRangedChange(t *testing.T) {
	text := "module app\n// é😀x\nfunc main() -> i32 {\n    return 0i32\n}\n"
	dir := writeTree(t, map[string]string{"app.kern": text})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	c := start(t, Options{})
	c.initialize(nil, true) // UTF-16
	c.open(uri, text)
	// replace the x after é😀: UTF-16 character 6 of line 1 ("// " 3 + é 1 + 😀 2)
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 2},
		"contentChanges": []any{map[string]any{"range": Range{Start: Position{1, 6}, End: Position{1, 7}}, "text": "y"}}})
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": 3},
		"contentChanges": []any{map[string]any{"range": Range{Start: Position{1, 5}, End: Position{1, 2}}, "text": "z"}}})
	var logged bool
	for !logged {
		m := c.next()
		if m.Method == "window/logMessage" {
			var p logMessageParams
			_ = json.Unmarshal(m.Params, &p)
			logged = p.Type == 2
		}
	}
	c.reply(c.request("textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": uri}, "position": Position{}}))
	if d := c.srv.docs[uri]; string(d.text) != strings.Replace(text, "x", "y", 1) || d.version != 2 {
		t.Errorf("text %q version %d", d.text, d.version)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

func (c *client) actions(uri string, r Range) []CodeAction {
	c.t.Helper()
	var as []CodeAction
	if e := c.call("textDocument/codeAction", map[string]any{"textDocument": map[string]any{"uri": uri}, "range": r, "context": map[string]any{"diagnostics": []any{}}}, &as); e != nil {
		c.t.Fatalf("codeAction: %+v", e)
	}
	return as
}

// TestQuickFix: a resolver fix and a checker fix are offered as quick
// fixes whose edits make the program check clean (R7).
func TestQuickFix(t *testing.T) {
	for _, k := range []struct {
		name, text, code string
	}{
		{"resolver", "module app\nfunc f(v: Vec[u64]) { }\n", "PARAM_OWN_REQUIRED"},
		{"checker", "module app\nstruct P { x: u64 }\nfunc init_p(p: out P) -> Result[(), u8] { p.x = 1u64; return Ok(()) }\nfunc g() -> Result[u64, u8] {\n    var p: P\n    init_p(out p)\n    return Ok(7u64)\n}\n", "INIT_OUT_RESULT_UNHANDLED"},
	} {
		dir := writeTree(t, map[string]string{"app.kern": k.text})
		path := filepath.Join(dir, "app.kern")
		uri := URIOf(path)
		c := start(t, Options{})
		c.initialize(nil, true)
		c.open(uri, k.text)
		whole := Range{End: Position{Line: 99}}
		as := c.actions(uri, whole) // the document is stale or fresh: R7 checks it first
		if len(as) != 1 || as[0].Kind != "quickfix" || as[0].Title != "Apply the compiler's fix for "+k.code || len(as[0].Diagnostics) != 1 || as[0].Diagnostics[0].Code != k.code {
			t.Fatalf("%s: actions %+v", k.name, as)
		}
		edits := as[0].Edit.Changes[uri]
		if len(edits) != 1 || len(as[0].Edit.Changes) != 1 {
			t.Fatalf("%s: changes %+v", k.name, as[0].Edit.Changes)
		}
		fixed := applyEdits(k.text, edits, UTF16)
		if ds := uncachedCheck([]*source.File{{Name: path, Bytes: []byte(fixed)}}); len(ds) != 0 {
			t.Errorf("%s: the fixed text %q does not check clean: %v", k.name, fixed, ds)
		}
		// an only filter without quickfix: nothing; with a prefix of it: the fix
		for only, n := range map[string]int{"refactor": 0, "source.organizeImports": 0, "": 1, "quickfix": 1} {
			var as []CodeAction
			if e := c.call("textDocument/codeAction", map[string]any{"textDocument": map[string]any{"uri": uri}, "range": whole, "context": map[string]any{"diagnostics": []any{}, "only": []string{only}}}, &as); e != nil || len(as) != n {
				t.Errorf("%s: only %q gave %d actions (%+v), want %d", k.name, only, len(as), e, n)
			}
		}
		// a range away from the diagnostic: nothing
		if as := c.actions(uri, Range{Start: Position{Line: 50}, End: Position{Line: 60}}); len(as) != 0 {
			t.Errorf("%s: actions away from the diagnostic %+v", k.name, as)
		}
		if s := c.stop(); s != 0 {
			t.Errorf("status %d", s)
		}
	}
	// a client without codeActionLiteralSupport gets []
	text := "module app\nfunc f(v: Vec[u64]) { }\n"
	dir := writeTree(t, map[string]string{"app.kern": text})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	c := start(t, Options{})
	c.initialize(nil, false)
	c.open(uri, text)
	if as := c.actions(uri, Range{End: Position{Line: 99}}); len(as) != 0 {
		t.Errorf("actions without literal support %+v", as)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}
