package main

import (
	"errors"
	"fmt"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/source"
)

// hostedDeps is what the hosted-only evaluation of a freestanding program
// calls (specs/freestanding-availability F3): lower for the show module
// and analyze for the entries. check, build and run pass hostedDefaults;
// tests pass counting or failing wrappers.
type hostedDeps struct {
	lower   lowerFunc
	analyze func(mod *ir.Module, triple string) (analysis.HostedResult, error)
}

// hostedDefaults are the evaluation's real dependencies.
func hostedDefaults() hostedDeps {
	return hostedDeps{lower: Build, analyze: analysis.HostedOnly}
}

// freestandingTriple is the target a freestanding command builds for: the
// one given (--target or the manifest's), else the default bare-metal
// target.
func freestandingTriple(given string) string {
	if given != "" {
		return given
	}
	return native.FreestandingTriple
}

// hostedDiags evaluates a freestanding program that resolved and checked
// clean: the ENV_HOSTED_ONLY diagnostics of its files other than its test
// files (Rule 6.8), or lowering's own diagnostics unchanged. A lowering or
// analysis error, a missing module or a panic is an InternalError, and the
// caller then neither builds nor runs.
func hostedDiags(srcs []*source.File, optLevel, triple string, deps hostedDeps) (ds []diag.Diagnostic, err error) {
	defer func() {
		if r := recover(); r != nil {
			ds, err = nil, &InternalError{Err: fmt.Errorf("hosted-only evaluation: %v", r)}
		}
	}()
	var program []*source.File
	for _, s := range srcs {
		if !project.TestFile(s.Name) {
			program = append(program, s)
		}
	}
	mod, lds, lerr := deps.lower(program, optLevel)
	switch {
	case lerr != nil:
		return nil, &InternalError{Err: fmt.Errorf("hosted-only evaluation: lowering: %w", lerr)}
	case len(lds) > 0:
		return lds, nil
	case mod == nil:
		return nil, &InternalError{Err: errors.New("hosted-only evaluation: lowering returned no module")}
	}
	res, aerr := deps.analyze(mod, triple)
	if aerr != nil {
		return nil, &InternalError{Err: aerr}
	}
	return res.Diagnostics, nil
}
