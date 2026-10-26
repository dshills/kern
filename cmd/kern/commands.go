package main

import (
	"io"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// phase2Commands lists the machine-API commands available through `run`
// (`run` and `build` are dispatched before it).
var phase2Commands = map[string]bool{
	"check": true, "format": true, "emit-ir": true, "explain-ownership": true,
	"show-layout": true, "show-copies": true, "show-allocations": true, "show-bounds-checks": true, "emit-asm": true,
}

type command func(req Request, srcs []*source.File, stdout, stderr io.Writer) int

// analysisCommands are the §16 commands of PLAN.md 2.5.
var analysisCommands = map[string]command{
	"show-layout":        showLayout,
	"show-copies":        showCopies,
	"show-allocations":   showAllocations,
	"show-bounds-checks": showBoundsChecks,
	"emit-asm":           emitAsm,
}

// respond writes a success response, or the diagnostics with exit 2.
func respond(stdout io.Writer, result any, ds []diag.Diagnostic) int {
	if len(ds) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	if err := diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: true, Result: result, Diagnostics: []diag.Diagnostic{}}); err != nil {
		return 3
	}
	return 0
}

func showLayout(req Request, srcs []*source.File, stdout, stderr io.Writer) int {
	prog, ds, err := resolveChecked(srcs)
	if err != nil {
		return internal(req, err, stdout, stderr)
	}
	if len(ds) > 0 {
		return respond(stdout, nil, ds)
	}
	return respond(stdout, analysis.Layout(prog, req.Target), nil)
}

// resolveChecked resolves and checks, converting panics to InternalError.
func resolveChecked(srcs []*source.File) (prog *resolve.Program, ds []diag.Diagnostic, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &InternalError{Err: errorf("%v", r)}
		}
	}()
	prog = resolve.Resolve(srcs)
	if len(prog.Diags) > 0 {
		return prog, prog.Diags, nil
	}
	return prog, check.Check(prog), nil
}

func showCopies(req Request, srcs []*source.File, stdout, stderr io.Writer) int {
	mod, ds, err := Build(srcs)
	if err != nil {
		return internal(req, err, stdout, stderr)
	}
	if len(ds) > 0 {
		return respond(stdout, nil, ds)
	}
	return respond(stdout, analysis.Copies(mod, req.Target), nil)
}

func showAllocations(req Request, srcs []*source.File, stdout, stderr io.Writer) int {
	mod, ds, err := Build(srcs)
	if err != nil {
		return internal(req, err, stdout, stderr)
	}
	if len(ds) > 0 {
		return respond(stdout, nil, ds)
	}
	return respond(stdout, analysis.Allocations(mod), nil)
}

func showBoundsChecks(req Request, srcs []*source.File, stdout, stderr io.Writer) int {
	mod, ds, err := Build(srcs)
	if err != nil {
		return internal(req, err, stdout, stderr)
	}
	if len(ds) > 0 {
		return respond(stdout, nil, ds)
	}
	return respond(stdout, analysis.BoundsChecks(mod), nil)
}

func emitAsm(req Request, srcs []*source.File, stdout, stderr io.Writer) int {
	asm, ds, err := native.EmitAsm(srcs, req.Target)
	if err != nil {
		return internal(req, &InternalError{Err: err}, stdout, stderr)
	}
	if len(ds) > 0 {
		return respond(stdout, nil, ds)
	}
	return respond(stdout, diag.EmitAsmResult{Target: req.Target, Asm: asm}, nil)
}
