package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// queueSize is how many read messages wait for the loop (spec glossary,
// batch).
const queueSize = 64

// Options configures a server; the zero value is the production server.
type Options struct {
	// OnCheck is called once per program check with the document's URI.
	OnCheck func(uri string)
	// Check replaces resolution and checking of a program's files.
	Check func([]*source.File) ([]diag.Diagnostic, error)
	// Resolve replaces the resolution hover runs.
	Resolve func([]*source.File) *resolve.Program
	// Log receives the server's log lines; nil is standard error.
	Log io.Writer
	// Start, when not nil, holds the loop (not the reader) until it is
	// closed, so a test can queue a batch.
	Start <-chan struct{}
}

// lifecycle states (spec §3.1)
const (
	stateNew = iota
	stateRunning
	stateShutdown
)

type inbound struct {
	body []byte
	err  error
}

// document is an open document (spec §3.3).
type document struct {
	uri     string
	path    string // cleaned absolute path; "" for a non-file URI
	text    []byte
	version int
	seq     int // order of the last open or change, for the alias rule
}

// kern reports whether the document is checked: a file URI ending in .kern.
func (d *document) kern() bool { return d.path != "" && strings.HasSuffix(d.path, ".kern") }

// published is what the server last published on one URI.
type published struct {
	kern   []diag.Diagnostic // with absolute file names, after relocation
	lsp    []Diagnostic
	owners map[string]bool // documents whose last check published this URI
}

// Server is one language server session.
type Server struct {
	in    io.Reader
	out   io.Writer
	opts  Options
	log   io.Writer
	queue chan inbound
	done  chan struct{}

	wmu      sync.Mutex
	writeErr error

	state   int
	enc     Encoding
	literal bool // the client declared codeActionLiteralSupport

	docs    map[string]*document // by URI
	seq     int
	stale   map[string]bool
	pubs    map[string]*published      // by URI
	docPubs map[string]map[string]bool // document URI -> URIs its last check published
}

// New makes a server reading in and writing out.
func New(in io.Reader, out io.Writer, opts Options) *Server {
	if opts.Check == nil {
		opts.Check = defaultCheck
	}
	if opts.Resolve == nil {
		opts.Resolve = resolve.Resolve
	}
	log := opts.Log
	if log == nil {
		log = os.Stderr
	}
	return &Server{
		in: in, out: out, opts: opts, log: log,
		queue: make(chan inbound, queueSize), done: make(chan struct{}),
		docs: map[string]*document{}, stale: map[string]bool{},
		pubs: map[string]*published{}, docPubs: map[string]map[string]bool{},
	}
}

// Queued is the number of read messages waiting for the loop.
func (s *Server) Queued() int { return len(s.queue) }

// Run serves until exit or end of input and returns the exit status
// (spec §3.5). It does not wait for the reader, which stops at end of
// input, at a read error, or when Run has returned.
func (s *Server) Run() int {
	defer close(s.done)
	go s.read()
	if s.opts.Start != nil {
		<-s.opts.Start
	}
	for {
		var m inbound
		select {
		case m = <-s.queue:
		default:
			s.batchEnd()
			if s.writeErr != nil {
				return 1
			}
			m = <-s.queue
		}
		if m.err != nil {
			if !errors.Is(m.err, io.EOF) {
				s.logf("read: %v", m.err)
			}
			return s.exitStatus()
		}
		if exit := s.handle(m.body); exit {
			return s.exitStatus()
		}
		if s.writeErr != nil {
			return 1
		}
	}
}

func (s *Server) exitStatus() int {
	if s.state == stateShutdown {
		return 0
	}
	return 1
}

// read decodes framed messages into the queue until end of input or an
// error, which it queues last.
func (s *Server) read() {
	r := newReader(s.in)
	for {
		select {
		case <-s.done:
			return // Run has returned: start no further read
		default:
		}
		body, err := readMessage(r)
		select {
		case s.queue <- inbound{body: body, err: err}:
		case <-s.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// logf writes one log line.
func (s *Server) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.log, "kern lsp: "+format+"\n", args...)
}

// send writes one message; the first write error stops the server.
func (s *Server) send(v any, what string) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.writeErr != nil {
		return
	}
	if err := writeMessage(s.out, v); err != nil {
		s.writeErr = err
		s.logf("write %s: %v", what, err)
	}
}

func (s *Server) notify(method string, params any) {
	s.send(notification{JSONRPC: "2.0", Method: method, Params: params}, "method="+method)
}

func (s *Server) reply(id json.RawMessage, result any, rerr *rpcError) {
	r := response{JSONRPC: "2.0", ID: id, Error: rerr}
	if rerr == nil {
		b, err := json.Marshal(result)
		if err != nil {
			r.Error = &rpcError{Code: codeInternalError, Message: err.Error()}
		} else {
			r.Result = b
		}
	}
	s.send(r, "id="+string(id))
}

// logMessage sends window/logMessage and writes the same line to the log.
func (s *Server) logMessage(typ int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.logf("%s", msg)
	s.notify("window/logMessage", logMessageParams{Type: typ, Message: msg})
}

// handle handles one message body; it reports whether the server exits.
func (s *Server) handle(body []byte) bool {
	if !json.Valid(body) {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeParseError, Message: "the message is not JSON"})
		return false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeInvalidRequest, Message: "the message is not a JSON object"})
		return false
	}
	id, isRequest := raw["id"]
	var version string
	if json.Unmarshal(raw["jsonrpc"], &version) != nil || version != "2.0" || isRequest && !validID(id) {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeInvalidRequest, Message: `the message is not JSON-RPC 2.0 (jsonrpc "2.0", an id that is a string, a number or null)`})
		return false
	}
	var method string
	if err := json.Unmarshal(raw["method"], &method); err != nil || raw["method"] == nil || string(raw["method"]) == "null" {
		if !isRequest {
			id = json.RawMessage("null")
		}
		s.reply(id, nil, &rpcError{Code: codeInvalidRequest, Message: "the message has no method"})
		return false
	}
	params := raw["params"]
	if !isRequest {
		return s.notification(method, params)
	}
	switch {
	case s.state == stateNew && method != "initialize":
		s.reply(id, nil, &rpcError{Code: codeNotInitialized, Message: "the server is not initialized"})
	case s.state == stateShutdown:
		s.reply(id, nil, &rpcError{Code: codeInvalidRequest, Message: "the server is shut down"})
	case method == "initialize":
		if s.state != stateNew {
			s.reply(id, nil, &rpcError{Code: codeInvalidRequest, Message: "the server is already initialized"})
			return false
		}
		result, rerr := s.initialize(params)
		s.reply(id, result, rerr)
	case method == "shutdown":
		s.state = stateShutdown
		s.reply(id, nil, nil)
	default:
		result, rerr := s.request(method, params)
		s.reply(id, result, rerr)
	}
	return false
}

// validID reports whether a request id is a string, a number or null.
func validID(id json.RawMessage) bool {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return false
	}
	switch v.(type) {
	case nil, string, float64:
		return true
	}
	return false
}

// request answers one request of the running server, under recover
// (spec R5).
func (s *Server) request(method string, params json.RawMessage) (result any, rerr *rpcError) {
	defer func() {
		if r := recover(); r != nil {
			result, rerr = nil, &rpcError{Code: codeInternalError, Message: fmt.Sprint(r)}
			s.logf("request method=%s: panic: %v", method, r)
		}
	}()
	switch method {
	case "textDocument/formatting":
		var p documentFormattingParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParams(err)
		}
		return s.formatting(p)
	case "textDocument/hover":
		var p textDocumentPositionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParams(err)
		}
		return s.hover(p)
	case "textDocument/codeAction":
		var p codeActionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParams(err)
		}
		return s.codeAction(p)
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + method}
}

func invalidParams(err error) *rpcError {
	return &rpcError{Code: codeInvalidParams, Message: "invalid params: " + err.Error()}
}

// initialize negotiates the position encoding and returns the
// capabilities (spec §3.1).
func (s *Server) initialize(params json.RawMessage) (any, *rpcError) {
	var p initializeParams
	if len(params) > 0 && string(params) != "null" {
		// absent fields take the defaults; a field of the wrong JSON type
		// fails the request and the server stays uninitialized
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParams(err)
		}
	}
	s.enc = UTF16
	for _, e := range p.Capabilities.General.PositionEncodings {
		if e == "utf-8" {
			s.enc = UTF8
		}
	}
	lit := p.Capabilities.TextDocument.CodeAction.CodeActionLiteralSupport
	s.literal = len(lit) > 0 && string(lit) != "null"
	s.state = stateRunning
	// CodeActionOptions only for a client that declared literal support
	var codeActions any = true
	if s.literal {
		codeActions = map[string]any{"codeActionKinds": []string{"quickfix"}}
	}
	return map[string]any{
		"capabilities": map[string]any{
			"positionEncoding":           s.enc.String(),
			"textDocumentSync":           map[string]any{"openClose": true, "change": 1},
			"hoverProvider":              true,
			"documentFormattingProvider": true,
			"codeActionProvider":         codeActions,
		},
		"serverInfo": map[string]any{"name": "kern", "version": diag.SchemaVersion},
	}, nil
}

// notification handles one notification; it reports whether the server
// exits.
func (s *Server) notification(method string, params json.RawMessage) bool {
	if method == "exit" {
		return true
	}
	if s.state != stateRunning {
		return false
	}
	switch method {
	case "textDocument/didOpen":
		var p didOpenParams
		if err := json.Unmarshal(params, &p); err != nil {
			s.logf("notification method=%s: %v", method, err)
			return false
		}
		path, _ := PathOf(p.TextDocument.URI)
		s.seq++
		s.docs[p.TextDocument.URI] = &document{uri: p.TextDocument.URI, path: path, text: []byte(p.TextDocument.Text), version: p.TextDocument.Version, seq: s.seq}
		s.markStale()
	case "textDocument/didChange":
		var p didChangeParams
		if err := json.Unmarshal(params, &p); err != nil {
			s.logf("notification method=%s: %v", method, err)
			return false
		}
		d := s.docs[p.TextDocument.URI]
		if d == nil {
			return false
		}
		text := d.text
		for _, c := range p.ContentChanges {
			if c.Range == nil {
				text = []byte(c.Text)
				continue
			}
			if before(c.Range.End, c.Range.Start) {
				s.logMessage(2, "didChange of %s ignored: a change's range starts after its end", d.uri)
				return false
			}
			start, end := ToOffset(text, c.Range.Start, s.enc), ToOffset(text, c.Range.End, s.enc)
			next := make([]byte, 0, len(text)-(end-start)+len(c.Text))
			next = append(append(append(next, text[:start]...), c.Text...), text[end:]...)
			text = next
		}
		s.seq++
		d.text, d.version, d.seq = text, p.TextDocument.Version, s.seq
		s.markStale()
	case "textDocument/didClose":
		var p didCloseParams
		if err := json.Unmarshal(params, &p); err != nil {
			s.logf("notification method=%s: %v", method, err)
			return false
		}
		if s.docs[p.TextDocument.URI] == nil {
			return false
		}
		s.close(p.TextDocument.URI)
		s.markStale()
	}
	// didSave, initialized, $/cancelRequest, $/setTrace and any other
	// notification are ignored
	return false
}

// before reports whether position a is before b.
func before(a, b Position) bool {
	return a.Line < b.Line || a.Line == b.Line && a.Character < b.Character
}

// markStale marks every kern document stale (spec R1).
func (s *Server) markStale() {
	for uri, d := range s.docs {
		if d.kern() {
			s.stale[uri] = true
		}
	}
}

// batchEnd checks every stale kern document in URI order (spec R1).
func (s *Server) batchEnd() {
	var uris []string
	for uri := range s.stale {
		if d := s.docs[uri]; d != nil && d.kern() {
			uris = append(uris, uri)
		}
	}
	sort.Strings(uris)
	s.stale = map[string]bool{}
	for _, uri := range uris {
		s.checkDoc(s.docs[uri])
	}
}
