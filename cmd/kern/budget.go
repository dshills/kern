package main

import (
	"errors"
	"fmt"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// lowerFunc builds the show module of files at an opt level, as Build
// does: a module, or the resolution and checking diagnostics, or an error.
// check and build pass Build; tests pass a counting wrapper.
type lowerFunc func(srcs []*source.File, optLevel string) (*ir.Module, []diag.Diagnostic, error)

// requestHasBudgets parses the request's files, nothing else, and reports
// whether one declares a budget block (spec §7.6, R6): a request without
// one is never resolved again or lowered for its budgets.
func requestHasBudgets(srcs []*source.File) bool {
	var trees []*ast.File
	for _, s := range srcs {
		if f, ds := parser.Parse(s); len(ds) == 0 && f != nil {
			trees = append(trees, f)
		}
	}
	return analysis.HasBudgets(trees)
}

// budgetDiags evaluates the budgets of a program that resolved and
// checked clean (spec §7.6, R3 to R6): it lowers the show module of srcs
// at optLevel with lower and counts against it, the target going to the
// copies query as show-copies passes it. Diagnostics lower returns (the
// program did not check after all) are returned unchanged and no budget
// is evaluated; a lowering error, a missing module or a panic is an
// InternalError.
func budgetDiags(srcs []*source.File, optLevel, target string, lower lowerFunc) (ds []diag.Diagnostic, err error) {
	defer func() {
		if r := recover(); r != nil {
			ds, err = nil, &InternalError{Err: fmt.Errorf("budget evaluation: %v", r)}
		}
	}()
	if !requestHasBudgets(srcs) {
		return nil, nil
	}
	mod, lds, lerr := lower(srcs, optLevel)
	switch {
	case lerr != nil:
		return nil, &InternalError{Err: fmt.Errorf("budget evaluation: lowering: %w", lerr)}
	case len(lds) > 0:
		return lds, nil
	case mod == nil:
		return nil, &InternalError{Err: errors.New("budget evaluation: lowering returned no module")}
	}
	prog := resolve.ResolveFor(srcs, optLevel == "release")
	if len(prog.Diags) > 0 {
		return prog.Diags, nil
	}
	return analysis.Budgets(prog, srcs, mod, target), nil
}
