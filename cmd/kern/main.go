// Command kern is the kern compiler driver. Phase 0 provides `check`;
// `format` prints the canonical form of each file (spec/format.md).
//
// Machine-API form (spec §7): `kern check < request.json > response.json`.
// Convenience form: `kern check file.kern...` builds the request from the
// arguments. Exit codes: 0 success, 2 diagnostics or user error, 3 internal.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/dshills/kern/compiler/backend/native"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/format"
	"github.com/dshills/kern/compiler/incremental"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// Request is the §7 machine-API request (compiler/diag/schema.go).
type Request = diag.Request

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: kern <check|format|emit-ir|explain-ownership|run|build> [files...]")
		return 2
	}
	cmd := args[0]
	if cmd == "run" {
		return runProgram(args[1:], stdout, stderr)
	}
	if cmd == "build" {
		return buildCommand(args[1:], stdout, stderr)
	}
	if cmd == "debug" {
		return debugCommand(args[1:], stdout, stderr)
	}
	human := false
	cliTarget := ""
	cacheDir := ""
	lineSpan := 0
	cfg := incremental.Config{Opt: "debug", Env: "hosted"}
	var files []string
	for _, a := range args[1:] {
		if a == "--human" {
			human = true
			continue
		}
		if strings.HasPrefix(a, "--target=") {
			cliTarget = strings.TrimPrefix(a, "--target=")
			continue
		}
		if strings.HasPrefix(a, "--cache=") {
			cacheDir = strings.TrimPrefix(a, "--cache=")
			continue
		}
		if strings.HasPrefix(a, "--line=") {
			// explain-ownership convenience: the span of one source line of the first file
			if n, err := strconv.Atoi(strings.TrimPrefix(a, "--line=")); err == nil && n > 0 {
				lineSpan = n
			} else {
				return fail(stdout, "CLI_INVALID_FLAGS", "--line must be a positive integer")
			}
			continue
		}
		if strings.HasPrefix(a, "--opt=") || strings.HasPrefix(a, "--env=") {
			k, v, _ := strings.Cut(a, "=")
			if (k == "--opt" && v != "debug" && v != "release") || (k == "--env" && v != "hosted" && v != "freestanding") {
				return fail(stdout, "CLI_INVALID_FLAGS", "%s must be %s", k, map[string]string{"--opt": "debug or release", "--env": "hosted or freestanding"}[k])
			}
			if k == "--opt" {
				cfg.Opt = v
			} else {
				cfg.Env = v
			}
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			return fail(stdout, "CLI_INVALID_FLAGS", "unknown flag %s", a)
		}
		files = append(files, a)
	}
	if !phase2Commands[cmd] {
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
	if req.Command != "" && req.Command != cmd {
		return fail(stdout, "API_UNKNOWN_COMMAND", "unknown command %q", req.Command)
	}
	if len(req.Files) == 0 {
		return fail(stdout, "API_NO_FILES", "request names no files")
	}
	if cliTarget != "" && req.Target != "" && cliTarget != req.Target {
		return fail(stdout, "API_TARGET_CONFLICT", "CLI --target %s differs from the request target %s", cliTarget, req.Target)
	}
	if cliTarget != "" {
		req.Target = cliTarget
	}
	if req.Target == "" {
		req.Target = native.HostTriple()
	}
	var srcs []*source.File
	for _, name := range req.Files {
		f, err := source.Load(name)
		if err != nil {
			return fail(stdout, "API_FILE_NOT_FOUND", "cannot read %s: %v", name, err)
		}
		srcs = append(srcs, f)
	}
	if lineSpan > 0 && len(srcs) > 0 {
		req.Span = &diag.RequestSpan{File: srcs[0].Name, Start: lineStart(srcs[0].Bytes, lineSpan), End: lineEnd(srcs[0].Bytes, lineSpan)}
	}
	if cmd == "format" {
		return runFormat(req, srcs, human, stdout, stderr)
	}
	if cmd == "emit-ir" {
		return emitIR(req, srcs, stdout, stderr)
	}
	if cmd == "explain-ownership" {
		return explainOwnership(req, srcs, stdout, stderr)
	}
	if fn, ok := analysisCommands[cmd]; ok {
		return fn(req, srcs, stdout, stderr)
	}
	var ds []diag.Diagnostic
	var err error
	if cacheDir != "" {
		// incremental checking through the per-module cache (PLAN.md 4.1)
		cfg.Target = req.Target
		ds, _, err = incremental.Check(cacheDir, srcs, cfg)
		if err != nil {
			err = &InternalError{Err: err}
		}
	} else {
		ds, err = checkHook(srcs)
	}
	if err != nil {
		if code := internal(req, err, stdout, stderr); code != 0 {
			return code
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
	resp := diag.Response{SchemaVersion: diag.SchemaVersion, OK: len(ds) == 0, Result: diag.CheckResult{Files: req.Files}, Diagnostics: ds}
	if err := diag.Write(stdout, resp); err != nil {
		return 3
	}
	if len(ds) > 0 {
		return 2
	}
	return 0
}

// runFormat formats every requested file. Any parse failure makes the whole
// request fail with the parse diagnostics (ok=false, exit 2) and no result.
func runFormat(req Request, srcs []*source.File, human bool, stdout, stderr io.Writer) int {
	files, ds, err := formatHook(srcs)
	if err != nil {
		if code := internal(req, err, stdout, stderr); code != 0 {
			return code
		}
	}
	if human {
		for _, d := range ds {
			_, _ = fmt.Fprintln(stdout, diag.Render(d))
		}
		for _, f := range files {
			_, _ = io.WriteString(stdout, f.Formatted)
		}
		if len(ds) > 0 {
			return 2
		}
		return 0
	}
	var result any
	if len(ds) == 0 {
		result = map[string]any{"files": files}
	}
	resp := diag.Response{SchemaVersion: diag.SchemaVersion, OK: len(ds) == 0, Result: result, Diagnostics: ds}
	if err := diag.Write(stdout, resp); err != nil {
		return 3
	}
	if len(ds) > 0 {
		return 2
	}
	return 0
}

// internal reports a compiler panic wrapped as InternalError (exit 3); it
// returns 0 for any other error.
func internal(req Request, err error, stdout, stderr io.Writer) int {
	var ie *InternalError
	if !errors.As(err, &ie) {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "{\"level\":\"error\",\"internal\":%q}\n", ie.Error())
	d := diag.New("INTERNAL", req.Files[0], diag.Span{}, "%v", ie)
	_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: []diag.Diagnostic{d}})
	return 3
}

func fail(w io.Writer, code diag.Code, format string, args ...any) int {
	d := diag.New(code, "", diag.Span{}, format, args...)
	_ = diag.Write(w, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: []diag.Diagnostic{d}})
	return 2
}

// checkHook is the check entry point; tests replace it to inject failures.
var checkHook = CheckFiles

// formatHook is the format entry point; tests replace it to inject failures.
var formatHook = FormatFiles

// FormattedFile is one entry of the `format` result (spec §16).
type FormattedFile struct {
	File      string `json:"file"`
	Formatted string `json:"formatted"`
}

// FormatFiles formats each file, converting panics to InternalError. When
// any file fails to parse it returns the parse diagnostics of every failing
// file, sorted, and no formatted files.
func FormatFiles(srcs []*source.File) (files []FormattedFile, ds []diag.Diagnostic, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &InternalError{Err: fmt.Errorf("%v", r)}
		}
	}()
	for _, f := range srcs {
		out, fds := format.Format(f)
		if len(fds) > 0 {
			ds = append(ds, fds...)
			continue
		}
		files = append(files, FormattedFile{File: f.Name, Formatted: string(out)})
	}
	if len(ds) > 0 {
		diag.Sort(ds)
		return nil, ds, nil
	}
	return files, nil, nil
}

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// lineStart and lineEnd give the byte span [start, end) of a 1-based line.
func lineStart(src []byte, line int) int {
	off := 0
	for n := 1; n < line && off < len(src); n++ {
		i := bytes.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	return off
}

func lineEnd(src []byte, line int) int {
	start := lineStart(src, line)
	if i := bytes.IndexByte(src[start:], '\n'); i >= 0 {
		return start + i
	}
	return len(src)
}

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
