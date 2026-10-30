package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLSPBinary starts the built kern binary as a language server on a
// two-module project (specs/language-server/SPEC.md §5 item 3): an
// imported module's diagnostic is published on that module's file, an
// unsaved buffer is checked instead of the file on disk, and shutdown
// then exit gives status 0.
func TestLSPBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the kern binary")
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "kern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	dir := filepath.Join(tmp, "proj")
	files := map[string]string{
		"kern.mod":     `{"entry": "main.kern"}`,
		"main.kern":    "module app\nimport \"lib\"\nfunc main() -> i32 {\n    return 0i32\n}\n",
		"lib/lib.kern": "module lib\nfunc F() -> u64 {\n    return nope\n}\n",
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
	cmd := exec.Command(bin, "lsp")
	cmd.Dir = tmp // the server finds the project from the document, not its working directory
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	// a failure anywhere below must not leave the server running
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		t.Logf("server exit: %v", <-waited)
	})
	msgs := make(chan map[string]json.RawMessage, 64)
	go func() {
		r := bufio.NewReader(stdout)
		for {
			n := 0
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					close(msgs)
					return
				}
				line = strings.TrimSpace(line)
				if line == "" {
					break
				}
				if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
					n, _ = strconv.Atoi(v)
				}
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(r, body); err != nil {
				close(msgs)
				return
			}
			var m map[string]json.RawMessage
			_ = json.Unmarshal(body, &m)
			msgs <- m
		}
	}()
	send := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n%s", len(b), b); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]json.RawMessage {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatalf("the server closed its output; stderr: %s", stderr.String())
			}
			return m
		case <-time.After(30 * time.Second):
			t.Fatalf("no message; stderr: %s", stderr.String())
		}
		return nil
	}
	uri := func(name string) string {
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, name))}).String()
	}
	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"capabilities": map[string]any{}}})
	if m := next(); string(m["id"]) != "1" || m["error"] != nil {
		t.Fatalf("initialize: %v", m)
	}
	send(map[string]any{"method": "initialized", "params": map[string]any{}})
	// the buffer is unsaved and has an error the file on disk does not
	unsaved := "module app\nimport \"lib\"\nfunc main() -> i32 {\n    return missing\n}\n"
	send(map[string]any{"method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{"uri": uri("main.kern"), "languageId": "kern", "version": 1, "text": unsaved}}})
	got := map[string]string{}
	for len(got) < 2 {
		m := next()
		var p struct {
			URI         string `json:"uri"`
			Diagnostics []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"diagnostics"`
		}
		if err := json.Unmarshal(m["params"], &p); err != nil {
			t.Fatal(err)
		}
		var msgs []string
		for _, d := range p.Diagnostics {
			msgs = append(msgs, d.Code+": "+d.Message)
		}
		got[p.URI] = strings.Join(msgs, "; ")
	}
	if !strings.Contains(got[uri("lib/lib.kern")], "nope") {
		t.Errorf("lib diagnostics %q", got[uri("lib/lib.kern")])
	}
	if !strings.Contains(got[uri("main.kern")], "missing") {
		t.Errorf("main diagnostics %q: the unsaved buffer was not checked", got[uri("main.kern")])
	}
	send(map[string]any{"id": 2, "method": "shutdown"})
	if m := next(); string(m["id"]) != "2" || string(m["result"]) != "null" {
		t.Fatalf("shutdown: %v", m)
	}
	send(map[string]any{"method": "exit"})
	select {
	case err := <-waited:
		waited <- err // for the cleanup
		if err != nil {
			t.Errorf("exit: %v; stderr: %s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Errorf("the server did not exit; stderr: %s", stderr.String())
	}
	if code := run([]string{"lsp", "extra"}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Errorf("kern lsp with an argument: exit %d", code)
	}
}

// lockedBuffer is a buffer safe for the process's stderr copier and the
// test to use at once.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
