package main

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzRequest: the machine API never panics on untrusted request bytes and
// exits 0, 2 or 3 (docs/security-review.md).
func FuzzRequest(f *testing.F) {
	f.Add(`{"schema_version":"0.2","command":"check","files":["../../tests/run/hello.kern"]}`)
	f.Add(`{"schema_version":"0.2","command":"apply-structured-edit","files":["../../tests/run/hello.kern"],"edit":{"ops":[{"op":"insert","file":"../../tests/run/hello.kern","at":0,"text":"x"}]}}`)
	f.Add(`{"schema_version":"0.2","command":"explain-ownership","files":["../../tests/run/hello.kern"],"span":{"file":"../../tests/run/hello.kern","start":0,"end":5}}`)
	f.Add(`{`)
	f.Add(`[]`)
	f.Add(`{"schema_version":"0.2","command":"check","files":[]}`)
	f.Fuzz(func(t *testing.T, req string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on request %q: %v", req, r)
			}
		}()
		for _, cmd := range []string{"check", "explain-ownership", "apply-structured-edit", "show-layout"} {
			var out, errb bytes.Buffer
			code := run([]string{cmd}, strings.NewReader(req), &out, &errb)
			if code != 0 && code != 2 && code != 3 {
				t.Fatalf("exit %d on %q", code, req)
			}
		}
	})
}
