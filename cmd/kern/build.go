package main

import (
	"io"
	"path/filepath"
	"strings"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/project"
)

// buildCommand implements `kern build [--target=T] [--opt=O] [--env=E]
// [--test-hooks] [--emit-llvm=F] [-o out] [files...]`: the files default to
// the manifest's entry, and -o to its output (specs/module-discovery/SPEC.md).
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
	bo, named, _, err := parseRunArgs(rest)
	if err != nil {
		return fail(stdout, "CLI_INVALID_FLAGS", "%v", err)
	}
	pr, ok := load(named, stdout)
	if !ok {
		return 2
	}
	bo.applyManifest(pr.Manifest)
	if bo.TestHooks && bo.Opt == "release" {
		return fail(stdout, "CLI_INVALID_FLAGS", "--test-hooks requires --opt debug")
	}
	if m := pr.Manifest; m != nil {
		if opts.Triple == "" && m.Target != nil {
			opts.Triple = *m.Target
		}
		if out == "" && m.Output != nil {
			out = filepath.Join(pr.Root, filepath.FromSlash(*m.Output))
		}
	}
	opts.Opt = bo.Opt
	opts.TestHooks = bo.TestHooks
	opts.Freestanding = bo.Env == "freestanding"
	opts.NoStackCheck = bo.NoStackCheck
	opts.ElideChecks = bo.ElideChecks
	opts.Debug = bo.Debug
	if out == "" {
		return fail(stdout, "CLI_INVALID_FLAGS", "build requires -o <out> or an output in %s", project.ManifestName)
	}
	srcs := pr.Files
	req := Request{SchemaVersion: diag.SchemaVersion, Command: "build", Files: pr.Names()}
	ds, err := buildHook(srcs, out, opts)
	if err != nil {
		return internal(req, diag.SchemaVersion, &InternalError{Err: err}, stdout, stderr)
	}
	if len(ds) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	return 0
}

// buildHook is the native build entry point; tests replace it.
var buildHook = native.Build
