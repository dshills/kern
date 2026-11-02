package main

import (
	"errors"
	"fmt"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/project"
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
// checked clean (spec §7.6, R3 to R6). A function is counted over the
// show module of the program the show queries see: the files without
// their test files (*_test.kern, which only check loads), so a test's
// calls change no count. A function declared in a test file is counted
// over the program with its tests, the only one that has it, lowered
// only when a test file declares a budget. The target goes to the copies
// query as show-copies passes it. Diagnostics lower returns (the program
// did not check after all) are returned unchanged; a lowering error, a
// missing module or a panic is an InternalError.
func budgetDiags(srcs []*source.File, optLevel, target string, lower lowerFunc) (ds []diag.Diagnostic, err error) {
	defer func() {
		if r := recover(); r != nil {
			ds, err = nil, &InternalError{Err: fmt.Errorf("budget evaluation: %v", r)}
		}
	}()
	var program, tests []*source.File
	for _, s := range srcs {
		if project.TestFile(s.Name) {
			tests = append(tests, s)
		} else {
			program = append(program, s)
		}
	}
	if requestHasBudgets(program) {
		pds, err := evaluateBudgets(program, optLevel, target, lower, func(string) bool { return true })
		if err != nil || len(pds) > 0 && pds[0].Code != "BUDGET_EXCEEDED" {
			return pds, err
		}
		ds = pds
	}
	if len(tests) > 0 && requestHasBudgets(tests) {
		tds, err := evaluateBudgets(srcs, optLevel, target, lower, project.TestFile)
		if err != nil || len(tds) > 0 && tds[0].Code != "BUDGET_EXCEEDED" {
			return tds, err
		}
		ds = append(ds, tds...)
		diag.Sort(ds)
	}
	return ds, nil
}

// evaluateBudgets lowers files with lower and returns the budget
// diagnostics of the functions declared in the files keep accepts, or
// lower's own diagnostics unchanged.
func evaluateBudgets(files []*source.File, optLevel, target string, lower lowerFunc, keep func(string) bool) ([]diag.Diagnostic, error) {
	mod, lds, lerr := lower(files, optLevel)
	switch {
	case lerr != nil:
		return nil, &InternalError{Err: fmt.Errorf("budget evaluation: lowering: %w", lerr)}
	case len(lds) > 0:
		return lds, nil
	case mod == nil:
		return nil, &InternalError{Err: errors.New("budget evaluation: lowering returned no module")}
	}
	prog := resolve.ResolveFor(files, optLevel == "release")
	if len(prog.Diags) > 0 {
		return prog.Diags, nil
	}
	var out []diag.Diagnostic
	for _, d := range analysis.Budgets(prog, files, mod, target) {
		if keep(d.File) {
			out = append(out, d)
		}
	}
	return out, nil
}
