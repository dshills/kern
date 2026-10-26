// Command kern is the kern compiler driver. Phase 0 provides `check`.
//
// Machine-API form (spec §7): `kern check < request.json > response.json`.
// Convenience form: `kern check file.kern...` builds the request from the
// arguments. Exit codes: 0 success, 2 diagnostics or user error, 3 internal.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// Request is the §7 machine-API request.
type Request struct {
	SchemaVersion string   `json:"schema_version"`
	Command       string   `json:"command"`
	Files         []string `json:"files"`
	Target        string   `json:"target,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: kern <check> [files...]")
		return 2
	}
	cmd := args[0]
	human := false
	var files []string
	for _, a := range args[1:] {
		if a == "--human" {
			human = true
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			return fail(stdout, "CLI_INVALID_FLAGS", "unknown flag %s", a)
		}
		files = append(files, a)
	}
	if cmd != "check" {
		return fail(stdout, "API_UNKNOWN_COMMAND", "unknown command %q", cmd)
	}
	var req Request
	if len(files) > 0 {
		req = Request{SchemaVersion: diag.SchemaVersion, Command: cmd, Files: files}
	} else {
		dec := json.NewDecoder(stdin)
		if err := dec.Decode(&req); err != nil {
			return fail(stdout, "API_MALFORMED_REQUEST", "request is not valid JSON: %v", err)
		}
		if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
			return fail(stdout, "API_MALFORMED_REQUEST", "request must be exactly one JSON document")
		}
		if req.Command != "" && req.Command != cmd {
			return fail(stdout, "API_UNKNOWN_COMMAND", "request command %q does not match %q", req.Command, cmd)
		}
	}
	if req.SchemaVersion != diag.SchemaVersion {
		return fail(stdout, "API_UNSUPPORTED_SCHEMA", "schema_version %q is not supported (want %s)", req.SchemaVersion, diag.SchemaVersion)
	}
	if req.Command != "" && req.Command != "check" {
		return fail(stdout, "API_UNKNOWN_COMMAND", "unknown command %q", req.Command)
	}
	if len(req.Files) == 0 {
		return fail(stdout, "API_NO_FILES", "request names no files")
	}
	var srcs []*source.File
	for _, name := range req.Files {
		f, err := source.Load(name)
		if err != nil {
			return fail(stdout, "API_FILE_NOT_FOUND", "cannot read %s: %v", name, err)
		}
		srcs = append(srcs, f)
	}
	ds, err := checkHook(srcs)
	if err != nil {
		var ie *InternalError
		if errors.As(err, &ie) {
			_, _ = fmt.Fprintf(stderr, "{\"level\":\"error\",\"internal\":%q}\n", ie.Error())
			d := diag.New("INTERNAL", req.Files[0], diag.Span{}, "%v", ie)
			_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: []diag.Diagnostic{d}})
			return 3
		}
	}
	if human {
		for _, d := range ds {
			_, _ = fmt.Fprintln(stdout, diag.Render(d))
		}
		if len(ds) > 0 {
			return 2
		}
		return 0
	}
	resp := diag.Response{SchemaVersion: diag.SchemaVersion, OK: len(ds) == 0, Result: map[string]any{"files": req.Files}, Diagnostics: ds}
	if err := diag.Write(stdout, resp); err != nil {
		return 3
	}
	if len(ds) > 0 {
		return 2
	}
	return 0
}

func fail(w io.Writer, code diag.Code, format string, args ...any) int {
	d := diag.New(code, "", diag.Span{}, format, args...)
	_ = diag.Write(w, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: []diag.Diagnostic{d}})
	return 2
}

// checkHook is the check entry point; tests replace it to inject failures.
var checkHook = CheckFiles

// InternalError wraps a compiler panic.
type InternalError struct{ Err error }

func (e *InternalError) Error() string { return "internal: " + e.Err.Error() }
func (e *InternalError) Unwrap() error { return e.Err }

// CheckFiles resolves and checks files, converting panics to InternalError.
func CheckFiles(srcs []*source.File) (ds []diag.Diagnostic, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &InternalError{Err: fmt.Errorf("%v", r)}
		}
	}()
	prog := resolve.Resolve(srcs)
	if len(prog.Diags) > 0 {
		return prog.Diags, nil
	}
	return check.Check(prog), nil
}
