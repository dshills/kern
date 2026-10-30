package lsp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/source"
)

const goodMain = "module app\nfunc main() -> i32 {\n    return 0i32\n}\n"
const badMain = "module app\nfunc main() -> i32 {\n    return nope\n}\n"

func TestInitialize(t *testing.T) {
	for _, c := range []struct {
		offered []string
		want    string
	}{{[]string{"utf-16", "utf-8"}, "utf-8"}, {[]string{"utf-32"}, "utf-16"}, {nil, "utf-16"}} {
		cl := start(t, Options{})
		res := cl.initialize(c.offered, true)
		caps := res["capabilities"].(map[string]any)
		if caps["positionEncoding"] != c.want {
			t.Errorf("offered %v: encoding %v, want %s", c.offered, caps["positionEncoding"], c.want)
		}
		sync := caps["textDocumentSync"].(map[string]any)
		if sync["openClose"] != true || sync["change"] != float64(1) || caps["hoverProvider"] != true || caps["documentFormattingProvider"] != true {
			t.Errorf("capabilities %v", caps)
		}
		if kinds := fmt.Sprint(caps["codeActionProvider"]); kinds != "map[codeActionKinds:[quickfix]]" {
			t.Errorf("codeActionProvider %s", kinds)
		}
		cl2 := start(t, Options{})
		if caps := cl2.initialize(c.offered, false)["capabilities"].(map[string]any); caps["codeActionProvider"] != true {
			t.Errorf("codeActionProvider without literal support %v", caps["codeActionProvider"])
		}
		cl2.stop()
		if info := res["serverInfo"].(map[string]any); info["name"] != "kern" || info["version"] != diag.SchemaVersion || len(caps) != 7 || caps["definitionProvider"] != true || caps["referencesProvider"] != true {
			t.Errorf("serverInfo %v, %d capabilities", info, len(caps))
		}
		if s := cl.stop(); s != 0 {
			t.Errorf("exit status %d", s)
		}
	}
}

func TestLifecycle(t *testing.T) {
	dir := writeTree(t, map[string]string{"app.kern": badMain})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	c := start(t, Options{})
	if e := c.call("textDocument/hover", map[string]any{}, nil); e == nil || e.Code != codeNotInitialized {
		t.Errorf("request before initialize: %+v", e)
	}
	c.open(uri, badMain) // ignored before initialize
	c.initialize(nil, true)
	if e := c.call("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": uri}}, nil); e == nil || e.Code != codeInvalidParams {
		t.Errorf("the didOpen before initialize was not ignored: %+v", e)
	}
	if e := c.call("initialize", map[string]any{}, nil); e == nil || e.Code != codeInvalidRequest {
		t.Errorf("second initialize: %+v", e)
	}
	if e := c.call("workspace/symbol", map[string]any{}, nil); e == nil || e.Code != codeMethodNotFound {
		t.Errorf("unknown request: %+v", e)
	}
	// an unknown notification, $/cancelRequest and didSave get no answer:
	// the next message is the reply to the request after them
	c.notify("$/cancelRequest", map[string]any{"id": 99})
	c.notify("custom/thing", map[string]any{})
	c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}})
	id := c.request("shutdown", nil)
	if m := c.next(); string(m.ID) != fmt.Sprint(id) || m.Error != nil || string(m.Result) != "null" {
		t.Errorf("after ignored notifications: %+v", m)
	}
	if e := c.call("textDocument/hover", map[string]any{}, nil); e == nil || e.Code != codeInvalidRequest {
		t.Errorf("request after shutdown: %+v", e)
	}
	c.open(uri, badMain) // ignored after shutdown: no publication follows
	c.notify("exit", nil)
	if s := c.wait(); s != 0 {
		t.Errorf("exit after shutdown: status %d", s)
	}
	for m := range c.msgs {
		t.Errorf("message after shutdown: %+v", m)
	}
}

func TestExitStatus(t *testing.T) {
	c := start(t, Options{})
	c.initialize(nil, true)
	c.notify("exit", nil)
	if s := c.wait(); s != 1 {
		t.Errorf("exit without shutdown: %d", s)
	}
	c = start(t, Options{})
	c.initialize(nil, true)
	_ = c.in.Close()
	if s := c.wait(); s != 1 {
		t.Errorf("end of input without shutdown: %d", s)
	}
	c = start(t, Options{})
	c.initialize(nil, true)
	if e := c.call("shutdown", nil, nil); e != nil {
		t.Fatal(e)
	}
	_ = c.in.Close()
	if s := c.wait(); s != 0 {
		t.Errorf("end of input after shutdown: %d", s)
	}
}

func TestMalformedMessages(t *testing.T) {
	c := start(t, Options{})
	c.initialize(nil, true)
	for body, code := range map[string]int{"{not json": codeParseError, "[1,2]": codeInvalidRequest, "null": codeInvalidRequest, "42": codeInvalidRequest,
		`{"jsonrpc":"1.0","id":3,"method":"shutdown"}`: codeInvalidRequest, `{"id":3,"method":"shutdown"}`: codeInvalidRequest,
		`{"jsonrpc":"2.0","id":{"a":1},"method":"shutdown"}`: codeInvalidRequest, `{"jsonrpc":"2.0","id":[1],"method":"shutdown"}`: codeInvalidRequest} {
		c.raw(body)
		if m := c.next(); m.Error == nil || m.Error.Code != code || string(m.ID) != "null" {
			t.Errorf("body %q: %+v", body, m)
		}
	}
	for _, body := range []string{`{"jsonrpc":"1.0","method":"exit"}`, `{}`, `{"jsonrpc":"2.0","result":null}`, `{"jsonrpc":"2.0","method":null}`} {
		c.raw(body) // invalid without an id: answered with id null, not obeyed
		if m := c.next(); m.Error == nil || m.Error.Code != codeInvalidRequest || string(m.ID) != "null" {
			t.Errorf("body %q: %+v", body, m)
		}
	}
	c.raw(`{"jsonrpc":"2.0","id":7}`)
	if m := c.next(); m.Error == nil || m.Error.Code != codeInvalidRequest || string(m.ID) != "7" {
		t.Errorf("request without method: %+v", m)
	}
	// mixed-case header names and a Content-Type
	body := `{"jsonrpc":"2.0","id":9,"method":"shutdown"}`
	if _, err := fmt.Fprintf(c.in, "content-LENGTH: %d\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n%s", len(body), body); err != nil {
		t.Fatal(err)
	}
	if m := c.next(); string(m.ID) != "9" || m.Error != nil {
		t.Errorf("mixed-case header: %+v", m)
	}
	c.notify("exit", nil)
	if s := c.wait(); s != 0 {
		t.Errorf("status %d", s)
	}
}

func TestFramingErrors(t *testing.T) {
	for name, input := range map[string]string{
		"no Content-Length":  "Content-Type: x\r\n\r\n{}",
		"too long":           fmt.Sprintf("Content-Length: %d\r\n\r\n", maxBody+1),
		"end inside a body":  "Content-Length: 10\r\n\r\n{}",
		"end inside headers": "Content-Length: 2\r\n",
		"no colon":           "Content-Length 2\r\n\r\n{}",
		"long header line":   "X-Long: " + strings.Repeat("a", maxHeaderLine+10) + "\r\nContent-Length: 2\r\n\r\n{}",
		"many header lines":  strings.Repeat("X-A: b\r\n", maxHeaderLines+1) + "Content-Length: 2\r\n\r\n{}",
	} {
		var out bytes.Buffer
		var log syncBuffer
		s := New(strings.NewReader(input), &out, Options{Log: &log})
		if st := s.Run(); st != 1 || out.Len() != 0 {
			t.Errorf("%s: status %d, output %q", name, st, out.String())
		}
		if name != "end inside headers" && name != "end inside a body" && !strings.Contains(log.String(), "framing") {
			t.Errorf("%s: log %q", name, log.String())
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestWriteFailure(t *testing.T) {
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	more := `{"jsonrpc":"2.0","id":2,"method":"shutdown"}`
	in := fmt.Sprintf("Content-Length: %d\r\n\r\n%sContent-Length: %d\r\n\r\n%s", len(init), init, len(more), more)
	var log syncBuffer
	if st := New(strings.NewReader(in), failingWriter{}, Options{Log: &log}).Run(); st != 1 || !strings.Contains(log.String(), "broken pipe") {
		t.Errorf("status %d, log %q", st, log.String())
	}
}

// TestBatch: changes queued together are one batch and one check (R1);
// an error fixed is republished empty (R2).
func TestBatch(t *testing.T) {
	dir := writeTree(t, map[string]string{"app.kern": goodMain})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	startCh := make(chan struct{})
	c := start(t, Options{Start: startCh})
	initID := c.request("initialize", map[string]any{"capabilities": map[string]any{}}) // queued: the loop is held
	c.notify("initialized", map[string]any{})
	c.open(uri, goodMain)
	c.change(uri, 2, goodMain+"\n")
	c.change(uri, 3, badMain+"\n\n")
	c.change(uri, 4, badMain)
	deadline := time.Now().Add(10 * time.Second)
	for c.srv.Queued() < 6 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if q := c.srv.Queued(); q != 6 {
		t.Fatalf("queued %d, want 6", q)
	}
	close(startCh)
	if m, _ := c.reply(initID); m.Error != nil {
		t.Fatalf("initialize: %+v", m.Error)
	}
	p := c.publication(uri)
	if codesOf(p.Diagnostics) == "" || p.Version == nil || *p.Version != 4 {
		t.Fatalf("publication %+v", p)
	}
	c.reply(c.request("textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": "untitled:x"}, "position": Position{}})) // OnCheck runs after the publication
	if n := c.checks.get(uri); n != 1 {
		t.Errorf("%d checks for one batch, want 1", n)
	}
	d := p.Diagnostics[0]
	if d.Source != "kern" || d.Severity != 1 || !strings.Contains(d.Message, "(rule ") || d.Range.Start.Line != 2 {
		t.Errorf("diagnostic shape %+v", d)
	}
	c.change(uri, 5, goodMain)
	if p := c.publication(uri); len(p.Diagnostics) != 0 || *p.Version != 5 {
		t.Errorf("fixed error not cleared: %+v", p)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// TestRelocation: a diagnostic without a file or naming a file outside
// the program is published on the document at 0:0 (R2).
func TestRelocation(t *testing.T) {
	dir := writeTree(t, map[string]string{"app.kern": goodMain})
	path := filepath.Join(dir, "app.kern")
	uri := URIOf(path)
	check := func(srcs []*source.File) ([]diag.Diagnostic, error) {
		a := diag.New("INTERNAL", "", diag.Span{}, "no file")
		b := diag.New("INTERNAL", "/elsewhere/x.kern", diag.Span{Start: 3, End: 9}, "elsewhere")
		b.Related = []diag.Related{{Label: "here", Start: 1, End: 2}}
		in := diag.New("INTERNAL", path, diag.Span{Start: 11, End: 15}, "inside")
		in.Related = []diag.Related{{Label: "see", Start: 0, End: 6}}
		return []diag.Diagnostic{a, b, in}, nil
	}
	c := start(t, Options{Check: check})
	c.initialize(nil, true)
	c.open(uri, goodMain)
	p := c.publication(uri)
	if len(p.Diagnostics) != 3 {
		t.Fatalf("publication %+v", p)
	}
	zero := Range{}
	if d := p.Diagnostics[0]; d.Range != zero || d.Message != "no file (rule internal)" && !strings.HasPrefix(d.Message, "no file") {
		t.Errorf("file-less diagnostic %+v", d)
	}
	if d := p.Diagnostics[1]; d.Range != zero || !strings.HasPrefix(d.Message, "/elsewhere/x.kern: elsewhere") || d.RelatedInformation != nil {
		t.Errorf("foreign diagnostic %+v", d)
	}
	if d := p.Diagnostics[2]; d.Range.Start != (Position{1, 0}) || len(d.RelatedInformation) != 1 || d.RelatedInformation[0].Location.URI != uri || d.RelatedInformation[0].Message != "see" {
		t.Errorf("diagnostic in the file %+v", d)
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// TestImportedAndClose: a diagnostic in an imported module is published
// on that module's file; closing the only document that published it
// clears it, unless another open document's check published it too (R2,
// R4).
func TestImportedAndClose(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app.kern":     "module app\nimport \"lib\"\nfunc main() -> i32 {\n    return 0i32\n}\n",
		"lib/lib.kern": "module lib\nfunc F() -> u64 {\n    return nope\n}\n",
	})
	app, lib := URIOf(filepath.Join(dir, "app.kern")), URIOf(filepath.Join(dir, "lib", "lib.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(app, "module app\nimport \"lib\"\nfunc main() -> i32 {\n    return 0i32\n}\n")
	got := map[string]PublishDiagnosticsParams{}
	for len(got) < 2 {
		m := c.next()
		var p PublishDiagnosticsParams
		_ = json.Unmarshal(m.Params, &p)
		got[p.URI] = p
	}
	if len(got[app].Diagnostics) != 0 || codesOf(got[lib].Diagnostics) == "" || got[lib].Version != nil {
		t.Fatalf("publications %+v", got)
	}
	// close the only owner: both URIs are cleared
	c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": app}})
	cleared := map[string]int{}
	for len(cleared) < 2 {
		m := c.next()
		var p PublishDiagnosticsParams
		_ = json.Unmarshal(m.Params, &p)
		cleared[p.URI] = len(p.Diagnostics)
	}
	if cleared[app] != 0 || cleared[lib] != 0 {
		t.Errorf("after close %v", cleared)
	}
	// two owners: closing one keeps lib's diagnostics
	c.open(app, "module app\nimport \"lib\"\nfunc main() -> i32 {\n    return 0i32\n}\n")
	c.open(lib, "module lib\nfunc F() -> u64 {\n    return nope\n}\n")
	// fence requests until both documents' checks are done
	deadline := time.Now().Add(10 * time.Second)
	for (c.checks.get(app) < 2 || c.checks.get(lib) < 1) && time.Now().Before(deadline) {
		c.reply(c.request("textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": "untitled:x"}, "position": Position{}}))
	}
	if c.checks.get(app) < 2 || c.checks.get(lib) < 1 { // app is re-checked when lib opens in a later batch
		t.Fatalf("checks app %d lib %d", c.checks.get(app), c.checks.get(lib))
	}
	c.notify("textDocument/didClose", map[string]any{"textDocument": map[string]any{"uri": app}})
	_, notes := c.reply(c.request("textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": "untitled:x"}, "position": Position{}}))
	for _, m := range notes {
		var p PublishDiagnosticsParams
		_ = json.Unmarshal(m.Params, &p)
		if p.URI == lib && len(p.Diagnostics) == 0 {
			t.Errorf("lib cleared although its own document is open: %+v", notes)
		}
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// TestPanicContained: a panicking check becomes an INTERNAL diagnostic and
// a log message; the server keeps answering (R5).
func TestPanicContained(t *testing.T) {
	dir := writeTree(t, map[string]string{"app.kern": goodMain})
	uri := URIOf(filepath.Join(dir, "app.kern"))
	c := start(t, Options{Check: func([]*source.File) ([]diag.Diagnostic, error) { panic("boom") }})
	c.initialize(nil, true)
	c.open(uri, goodMain)
	var logged, published bool
	for !logged || !published {
		m := c.next()
		switch m.Method {
		case "window/logMessage":
			var p logMessageParams
			_ = json.Unmarshal(m.Params, &p)
			logged = p.Type == 1 && strings.Contains(p.Message, "boom")
		case "textDocument/publishDiagnostics":
			var p PublishDiagnosticsParams
			_ = json.Unmarshal(m.Params, &p)
			published = codesOf(p.Diagnostics) == "INTERNAL" && strings.Contains(p.Diagnostics[0].Message, "boom")
		}
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// TestReaderStops: Run returns while the reader is blocked on a full
// queue, or in Read on an open pipe, and the reader then ends.
func TestReaderStops(t *testing.T) {
	base := runtime.NumGoroutine()
	exit := `{"jsonrpc":"2.0","method":"exit"}`
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(exit), exit)
	// full queue: queueSize messages queued and one more held by the reader
	startCh := make(chan struct{})
	inR, inW := io.Pipe()
	s := New(inR, io.Discard, Options{Start: startCh, Log: io.Discard})
	status := make(chan int, 1)
	go func() { status <- s.Run() }()
	go func() { _, _ = io.WriteString(inW, strings.Repeat(frame, queueSize+1)) }()
	deadline := time.Now().Add(10 * time.Second)
	for s.Queued() < queueSize && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(startCh)
	if st := waitStatus(t, status); st != 1 {
		t.Errorf("status %d", st)
	}
	_ = inW.Close()
	// blocked in Read: the pipe stays open until after Run returned
	inR2, inW2 := io.Pipe()
	s2 := New(inR2, io.Discard, Options{Log: io.Discard})
	go func() { status <- s2.Run() }()
	if _, err := io.WriteString(inW2, frame); err != nil {
		t.Fatal(err)
	}
	if st := waitStatus(t, status); st != 1 {
		t.Errorf("status %d", st)
	}
	_ = inW2.Close()
	for time.Now().Before(deadline) && runtime.NumGoroutine() > base {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base {
		t.Errorf("%d goroutines, %d before", n, base)
	}
}

// TestInitializeInvalid: initialize params of the wrong shape fail with
// -32602 and leave the server uninitialized.
func TestInitializeInvalid(t *testing.T) {
	c := start(t, Options{})
	for _, params := range []any{[]int{1}, map[string]any{"capabilities": map[string]any{"general": map[string]any{"positionEncodings": "utf-8"}}}} {
		if e := c.call("initialize", params, nil); e == nil || e.Code != codeInvalidParams {
			t.Errorf("initialize %v: %+v", params, e)
		}
	}
	if e := c.call("textDocument/hover", map[string]any{}, nil); e == nil || e.Code != codeNotInitialized {
		t.Errorf("after a failed initialize: %+v", e)
	}
	c.initialize([]string{"utf-8"}, true)
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// TestSharedPublication: when one document's program stops including a
// file another open document's program still includes, the file's
// diagnostics stay (R2).
func TestSharedPublication(t *testing.T) {
	withImport := "module app\nimport \"lib\"\nfunc main() -> i32 {\n    return 0i32\n}\n"
	libText := "module lib\nfunc F() -> u64 {\n    return nope\n}\n"
	dir := writeTree(t, map[string]string{"app.kern": withImport, "lib/lib.kern": libText})
	app, lib := URIOf(filepath.Join(dir, "app.kern")), URIOf(filepath.Join(dir, "lib", "lib.kern"))
	c := start(t, Options{})
	c.initialize(nil, true)
	c.open(app, withImport)
	c.open(lib, libText)
	fence := func() []rpc {
		_, notes := c.reply(c.request("textDocument/hover", map[string]any{"textDocument": map[string]any{"uri": "untitled:x"}, "position": Position{}}))
		return notes
	}
	deadline := time.Now().Add(10 * time.Second)
	for (c.checks.get(app) < 1 || c.checks.get(lib) < 1) && time.Now().Before(deadline) {
		fence()
	}
	// app drops the import: lib is still in lib's own program
	before := c.checks.get(app)
	c.change(app, 2, goodMain)
	var notes []rpc
	for c.checks.get(app) <= before && time.Now().Before(deadline) { // the change re-checks app (and lib)
		notes = append(notes, fence()...)
	}
	notes = append(notes, fence()...)
	if c.checks.get(app) <= before {
		t.Fatal("the change was not checked")
	}
	for _, m := range notes {
		var p PublishDiagnosticsParams
		_ = json.Unmarshal(m.Params, &p)
		if p.URI == lib && len(p.Diagnostics) == 0 {
			t.Errorf("lib cleared while its own document still publishes it")
		}
	}
	if s := c.stop(); s != 0 {
		t.Errorf("status %d", s)
	}
}

// waitStatus receives an exit status, failing after a timeout.
func waitStatus(t *testing.T, status <-chan int) int {
	t.Helper()
	select {
	case st := <-status:
		return st
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
	return -1
}
