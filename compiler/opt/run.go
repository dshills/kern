package opt

import (
	"fmt"

	"github.com/dshills/kern/compiler/ir"
)

// Options selects the passes: Inline is on for `--opt release` and off for
// `--opt debug`, where every call stays so the debugger steps into helpers.
type Options struct {
	Inline bool
	// InlineLibrary copies small library bodies into their callers after
	// the proof pass (specs/inline-library): only where the program runs
	// (the interpreter, native builds, kern test), never for the show-*
	// commands, whose user-level facts read library calls as calls.
	InlineLibrary bool
}

// OptionsFor maps a build configuration's opt level to the passes the
// analysis commands see.
func OptionsFor(optLevel string) Options { return Options{Inline: optLevel == "release"} }

// ExecOptionsFor maps an opt level to the passes of a program that runs:
// OptionsFor's, and in release the library inliner.
func ExecOptionsFor(optLevel string) Options {
	return Options{Inline: optLevel == "release", InlineLibrary: optLevel == "release"}
}

// Run applies the passes to a lowered module in the fixed order
// validate, inline, validate, prove, validate, inline-library, validate
// (PLAN.md 6.1, specs/inline-library). An error
// names the pass after which the IR failed validation; nothing later runs.
func Run(mod *ir.Module, o Options) error {
	if errs := ir.Validate(mod); len(errs) > 0 {
		return fmt.Errorf("lowering: %w", errs[0])
	}
	if o.Inline {
		Inline(mod)
		if errs := ir.Validate(mod); len(errs) > 0 {
			return fmt.Errorf("inline: %w", errs[0])
		}
	}
	Prove(mod)
	if errs := ir.Validate(mod); len(errs) > 0 {
		return fmt.Errorf("prove: %w", errs[0])
	}
	if o.InlineLibrary {
		InlineLibrary(mod)
		if errs := ir.Validate(mod); len(errs) > 0 {
			return fmt.Errorf("inline-library: %w", errs[0])
		}
	}
	return nil
}
