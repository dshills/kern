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
	"github.com/dshills/kern/compiler/project"
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
		_, _ = fmt.Fprintln(stderr, "usage: kern <check|format|emit-ir|explain-ownership|show-*|run|build|test> [flags] [files...]")
		_, _ = fmt.Fprintln(stderr, "  files default to the entry of kern.mod, the manifest that marks the project root and may set entry, opt, env, target and output; imports are found under that root")
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
	if cmd == "test" {
		return testCommand(args[1:], stdout, stderr)
	}
	human := false
	cliTarget := ""
	cacheDir := ""
	lineSpan := 0
	wire := diag.SchemaVersion // the response's wire version: the request's once read, else the newest
	cfg := incremental.Config{Opt: "debug", Env: "hosted"}
	optSet, envSet := false, false
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
				cfg.Opt, optSet = v, true
			} else {
				cfg.Env, envSet = v, true
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
		switch err := dec.Decode(&req); {
		case errors.Is(err, io.EOF):
			// no request on standard input: the manifest's entry files
			req = Request{SchemaVersion: diag.SchemaVersion, Command: cmd}
		case err != nil:
			return fail(stdout, "API_MALFORMED_REQUEST", "request is not valid JSON: %v", err)
		default:
			if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
				return fail(stdout, "API_MALFORMED_REQUEST", "request must be exactly one JSON document")
			}
			// a request of a known version is answered at that version
			// from here on, failures included
			if diag.Supported(req.SchemaVersion) {
				wire = req.SchemaVersion
			}
			if req.Command != "" && req.Command != cmd {
				return failAt(stdout, wire, "API_UNKNOWN_COMMAND", "request command %q does not match %q", req.Command, cmd)
			}
		}
	}
	if !diag.Supported(req.SchemaVersion) {
		return fail(stdout, "API_UNSUPPORTED_SCHEMA", "schema_version %q is not supported (want one of %s)", req.SchemaVersion, strings.Join(diag.Accepted, ", "))
	}
	wire = req.SchemaVersion
	if req.Command != "" && req.Command != cmd {
		return failAt(stdout, wire, "API_UNKNOWN_COMMAND", "unknown command %q", req.Command)
	}
	if cliTarget != "" && req.Target != "" && cliTarget != req.Target {
		return failAt(stdout, wire, "API_TARGET_CONFLICT", "CLI --target %s differs from the request target %s", cliTarget, req.Target)
	}
	// the program's files: the request's, or the manifest's entry, and the
	// files of every module they import (specs/module-discovery/SPEC.md)
	cwd, _ := os.Getwd()
	if len(req.Files) == 0 {
		// an empty file list is the manifest's entry; without a manifest
		// the request names no files, as before discovery
		if _, _, found := project.FindRoot(cwd, nil); !found {
			return failAt(stdout, wire, "API_NO_FILES", "request names no files and no %s is in the working directory or its ancestors", project.ManifestName)
		}
	}
	var pr project.Result
	if cmd == "format" {
		pr = project.Entries(cwd, req.Files) // formatting rewrites the entry files and needs no other
	} else {
		pr = project.Load(cwd, req.Files)
	}
	if len(pr.Diags) > 0 {
		return endWith(human, wire, pr.Diags, stdout)
	}
	if m := pr.Manifest; m != nil {
		if cliTarget == "" && req.Target == "" && m.Target != nil {
			req.Target = *m.Target
		}
		if !optSet && m.Opt != nil {
			cfg.Opt = *m.Opt
		}
		if !envSet && m.Env != nil {
			cfg.Env = *m.Env
		}
	}
	cliOpt = cfg.Opt
	if cliTarget != "" {
		req.Target = cliTarget
	}
	if req.Target == "" {
		req.Target = native.HostTriple()
	}
	srcs := pr.Files
	req.Files = pr.Names()
	if lineSpan > 0 && len(srcs) > 0 {
		req.Span = &diag.RequestSpan{File: srcs[0].Name, Start: lineStart(srcs[0].Bytes, lineSpan), End: lineEnd(srcs[0].Bytes, lineSpan)}
	}
	if cmd == "format" {
		return runFormat(req, wire, srcs, human, stdout, stderr)
	}
	if cmd == "emit-ir" {
		return emitIR(req, wire, srcs, stdout, stderr)
	}
	if cmd == "explain-ownership" {
		return explainOwnership(req, wire, srcs, stdout, stderr)
	}
	if fn, ok := analysisCommands[cmd]; ok {
		return fn(req, wire, srcs, stdout, stderr)
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
		if code := internal(req, wire, err, stdout, stderr); code != 0 {
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
	resp := diag.Response{OK: len(ds) == 0, Result: diag.CheckResult{Files: req.Files}, Diagnostics: ds}
	if err := diag.WriteAt(stdout, wire, resp); err != nil {
		return 3
	}
	if len(ds) > 0 {
		return 2
	}
	return 0
}

// runFormat formats every requested file. Any parse failure makes the whole
// request fail with the parse diagnostics (ok=false, exit 2) and no result.
func runFormat(req Request, wire string, srcs []*source.File, human bool, stdout, stderr io.Writer) int {
	files, ds, err := formatHook(srcs)
	if err != nil {
		if code := internal(req, wire, err, stdout, stderr); code != 0 {
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
	resp := diag.Response{OK: len(ds) == 0, Result: result, Diagnostics: ds}
	if err := diag.WriteAt(stdout, wire, resp); err != nil {
		return 3
	}
	if len(ds) > 0 {
		return 2
	}
	return 0
}

// internal reports a compiler panic wrapped as InternalError (exit 3) at
// the wire version; it returns 0 for any other error.
func internal(req Request, wire string, err error, stdout, stderr io.Writer) int {
	var ie *InternalError
	if !errors.As(err, &ie) {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "{\"level\":\"error\",\"internal\":%q}\n", ie.Error())
	d := diag.New("INTERNAL", req.Files[0], diag.Span{}, "%v", ie)
	_ = diag.WriteAt(stdout, wire, diag.Response{OK: false, Result: nil, Diagnostics: []diag.Diagnostic{d}})
	return 3
}

// endWith writes diagnostics that end a command before its own work: the
// human rendering under --human, else the single JSON response at the
// wire version; exit 2.
func endWith(human bool, wire string, ds []diag.Diagnostic, w io.Writer) int {
	if human {
		for _, d := range ds {
			_, _ = fmt.Fprintln(w, diag.Render(d))
		}
		return 2
	}
	_ = diag.WriteAt(w, wire, diag.Response{OK: false, Result: nil, Diagnostics: ds})
	return 2
}

// fail writes one failure diagnostic at the newest wire version: for the
// commands without a request (run, build, debug) and for a machine
// command before its request's version is known.
func fail(w io.Writer, code diag.Code, format string, args ...any) int {
	return failAt(w, diag.SchemaVersion, code, format, args...)
}

// failAt writes one failure diagnostic at a wire version; exit 2.
func failAt(w io.Writer, wire string, code diag.Code, format string, args ...any) int {
	d := diag.New(code, "", diag.Span{}, format, args...)
	_ = diag.WriteAt(w, wire, diag.Response{OK: false, Result: nil, Diagnostics: []diag.Diagnostic{d}})
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
