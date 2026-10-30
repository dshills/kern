package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// rpc is a decoded message from the server.
type rpc struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// client drives a server over pipes.
type client struct {
	t      *testing.T
	in     *io.PipeWriter
	srv    *Server
	msgs   chan rpc
	status chan int
	nextID int
	log    *syncBuffer
	checks *counter
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type counter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *counter) add(uri string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n[uri]++
}

func (c *counter) get(uri string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[uri]
}

// start runs a server with opts (OnCheck and Log are set by the client)
// and returns its client. The server's loop starts at once unless opts
// carries Start.
func start(t *testing.T, opts Options) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, msgs: make(chan rpc, 1024), status: make(chan int, 1), log: &syncBuffer{}, checks: &counter{n: map[string]int{}}}
	opts.OnCheck = c.checks.add
	opts.Log = c.log
	c.srv = New(inR, outW, opts)
	go func() {
		c.status <- c.srv.Run()
		_ = outW.Close()
	}()
	go func() {
		r := newReader(outR)
		for {
			body, err := readMessage(r)
			if err != nil {
				close(c.msgs)
				return
			}
			var m rpc
			if err := json.Unmarshal(body, &m); err != nil {
				panic(err)
			}
			c.msgs <- m
		}
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return c
}

// raw writes a raw body in framing.
func (c *client) raw(body string) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) send(v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	c.raw(string(b))
}

// request sends a request and returns its id.
func (c *client) request(method string, params any) int {
	c.nextID++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	return c.nextID
}

func (c *client) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// next returns the next message, failing after a timeout.
func (c *client) next() rpc {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatalf("the server closed its output; log:\n%s", c.log.String())
		}
		return m
	case <-time.After(20 * time.Second):
		c.t.Fatalf("no message from the server; log:\n%s", c.log.String())
	}
	return rpc{}
}

// reply waits for the response to id, collecting the notifications
// before it.
func (c *client) reply(id int) (rpc, []rpc) {
	c.t.Helper()
	var notes []rpc
	for {
		m := c.next()
		if m.Method == "" && string(m.ID) == fmt.Sprint(id) {
			return m, notes
		}
		notes = append(notes, m)
	}
}

// call sends a request and decodes its result into out (nil to skip).
func (c *client) call(method string, params, out any) *rpcError {
	c.t.Helper()
	m, _ := c.reply(c.request(method, params))
	if m.Error != nil {
		return m.Error
	}
	if out != nil {
		if err := json.Unmarshal(m.Result, out); err != nil {
			c.t.Fatalf("%s result %s: %v", method, m.Result, err)
		}
	}
	return nil
}

// initialize runs initialize and initialized with the given encodings
// and code-action literal support.
func (c *client) initialize(encodings []string, literal bool) map[string]any {
	c.t.Helper()
	caps := map[string]any{"general": map[string]any{"positionEncodings": encodings}}
	if literal {
		caps["textDocument"] = map[string]any{"codeAction": map[string]any{"codeActionLiteralSupport": map[string]any{"codeActionKind": map[string]any{"valueSet": []string{"quickfix"}}}}}
	}
	var res map[string]any
	if e := c.call("initialize", map[string]any{"capabilities": caps}, &res); e != nil {
		c.t.Fatalf("initialize: %+v", e)
	}
	c.notify("initialized", map[string]any{})
	return res
}

// publication waits for the next publishDiagnostics on uri.
func (c *client) publication(uri string) PublishDiagnosticsParams {
	c.t.Helper()
	for {
		m := c.next()
		if m.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			c.t.Fatal(err)
		}
		if p.URI == uri {
			return p
		}
	}
}

func (c *client) open(uri, text string) {
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "kern", "version": 1, "text": text}})
}

func (c *client) change(uri string, version int, text string) {
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": uri, "version": version}, "contentChanges": []any{map[string]any{"text": text}}})
}

// stop shuts the server down and returns its exit status.
func (c *client) stop() int {
	c.t.Helper()
	if e := c.call("shutdown", nil, nil); e != nil {
		c.t.Fatalf("shutdown: %+v", e)
	}
	c.notify("exit", nil)
	return c.wait()
}

func (c *client) wait() int {
	c.t.Helper()
	select {
	case s := <-c.status:
		return s
	case <-time.After(20 * time.Second):
		c.t.Fatal("the server did not exit")
	}
	return -1
}

// writeTree writes files under a new temporary directory and returns it,
// with symbolic links resolved so paths match the server's.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func codesOf(ds []Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Code)
	}
	return strings.Join(out, ",")
}
