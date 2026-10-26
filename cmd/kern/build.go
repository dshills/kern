package main

import (
	"io"
	"strings"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/source"
)

// buildCommand implements `kern build [--target=T] [--opt=O] [--env=E]
// [--test-hooks] [--emit-llvm=F] -o out files...`.
func buildCommand(args []string, stdout, stderr io.Writer) int {
	out := ""
	opts := native.Options{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" && i+1 < len(args):
			out = args[i+1]
			i++
		case strings.HasPrefix(a, "--emit-llvm="):
			opts.EmitLLVM = strings.TrimPrefix(a, "--emit-llvm=")
		case strings.HasPrefix(a, "--target="):
			opts.Triple = strings.TrimPrefix(a, "--target=")
		default:
			rest = append(rest, a)
		}
	}
	bo, files, _, err := parseRunArgs(rest)
	if err != nil {
		return fail(stdout, "CLI_INVALID_FLAGS", "%v", err)
	}
	opts.Opt = bo.Opt
	opts.TestHooks = bo.TestHooks
	if out == "" {
		return fail(stdout, "CLI_INVALID_FLAGS", "build requires -o <out>")
	}
	if len(files) == 0 {
		return fail(stdout, "API_NO_FILES", "build names no files")
	}
	var srcs []*source.File
	for _, name := range files {
		f, err := source.Load(name)
		if err != nil {
			return fail(stdout, "API_FILE_NOT_FOUND", "cannot read %s: %v", name, err)
		}
		srcs = append(srcs, f)
	}
	req := Request{SchemaVersion: diag.SchemaVersion, Command: "build", Files: files}
	ds, err := buildHook(srcs, out, opts)
	if err != nil {
		return internal(req, &InternalError{Err: err}, stdout, stderr)
	}
	if len(ds) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	return 0
}

// buildHook is the native build entry point; tests replace it.
var buildHook = native.Build
