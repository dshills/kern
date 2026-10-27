package opt

import (
	"fmt"

	"github.com/dshills/kern/compiler/ir"
)

// Options selects the passes: Inline is on for `--opt release` and off for
// `--opt debug`, where every call stays so the debugger steps into helpers.
type Options struct {
	Inline bool
}

// OptionsFor maps a build configuration's opt level to the passes.
func OptionsFor(optLevel string) Options { return Options{Inline: optLevel == "release"} }

// Run applies the passes to a lowered module in the fixed order
// validate, inline, validate, prove, validate (PLAN.md 6.1). An error
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
	return nil
}
