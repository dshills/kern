package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/project"
)

// buildCommand implements `kern build [--target=T] [--opt=O] [--env=E]
// [--test-hooks] [--emit-llvm=F] [-o out] [files...]`: the files default to
// the manifest's entry, and -o to its output (specs/module-discovery/SPEC.md).
// A freestanding build also takes --board=<file.c>, --linker-script=<file.ld>
// and --heap-bytes=N (specs/runtime RJ1).
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
	if bo.stackSet {
		opts.StackBytes = uint64(bo.StackBytes)
	}
	opts.Backtrace = bo.Backtrace
	// the board file, the linker script and the heap of a freestanding
	// image (specs/runtime RJ1): a flag, else the manifest's value, whose
	// paths are relative to the project root
	if opts.Freestanding {
		opts.Board, opts.LinkerScript, opts.HeapBytes, opts.Startup = bo.Board, bo.LinkerScript, uint64(bo.HeapBytes), bo.Startup
		if m := pr.Manifest; m != nil {
			if !bo.boardFlag && m.Board != nil {
				opts.Board = filepath.Join(pr.Root, filepath.FromSlash(*m.Board))
			}
			if !bo.scriptFlag && m.LinkerScript != nil {
				opts.LinkerScript = filepath.Join(pr.Root, filepath.FromSlash(*m.LinkerScript))
			}
			if !bo.heapFlag && m.HeapBytes != nil {
				opts.HeapBytes = uint64(*m.HeapBytes)
			}
			if !bo.startupFlag && m.Startup != nil {
				opts.Startup = filepath.Join(pr.Root, filepath.FromSlash(*m.Startup))
			}
		}
		for _, f := range []struct{ flag, path string }{{"--board", opts.Board}, {"--linker-script", opts.LinkerScript}, {"--startup", opts.Startup}} {
			if f.path == "" {
				continue
			}
			if st, err := os.Stat(f.path); err != nil || st.IsDir() {
				return fail(stdout, "CLI_INVALID_FLAGS", "%s: %s is not a file", f.flag, f.path)
			}
		}
	} else if bo.boardFlag || bo.scriptFlag || bo.heapFlag || bo.startupFlag {
		return fail(stdout, "CLI_INVALID_FLAGS", "--board, --linker-script, --startup and --heap-bytes configure a freestanding image: they need --env=freestanding")
	}
	if out == "" {
		return fail(stdout, "CLI_INVALID_FLAGS", "build requires -o <out> or an output in %s", project.ManifestName)
	}
	srcs := pr.Files
	req := Request{SchemaVersion: diag.SchemaVersion, Command: "build", Files: pr.Names()}
	// budgets first, at the build's level and target, so that a program
	// over its budget writes no output (spec §7.6); a program without a
	// budget block goes straight to the build
	target := opts.Triple
	if target == "" {
		target = native.HostTriple()
	}
	bds, err := budgetDiags(srcs, opts.Opt, target, budgetDefaults())
	if err != nil {
		return internal(req, diag.SchemaVersion, err, stdout, stderr)
	}
	if len(bds) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: bds})
		return 2
	}
	if opts.Freestanding {
		// before anything is compiled or written (Rule 6.8)
		hds, err := hostedDiags(srcs, opts.Opt, freestandingTriple(opts.Triple), buildHosted)
		if err != nil {
			return internal(req, diag.SchemaVersion, err, stdout, stderr)
		}
		if len(hds) > 0 {
			_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: hds})
			return 2
		}
	}
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

// buildHosted is build's hosted-only evaluation; tests replace it.
var buildHosted = hostedDefaults()
