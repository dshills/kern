package tests

import (
	"testing"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// TestStdModulesCheck: every embedded std module resolves and checks
// cleanly when a program imports it. A std module is parsed only on
// import, so a module no fixture imports would otherwise go unchecked.
func TestStdModulesCheck(t *testing.T) {
	for _, name := range resolve.StdModules {
		src := "module t\nimport \"std/" + name + "\"\nfunc main() -> i32 { return 0i32 }\n"
		prog := resolve.Resolve([]*source.File{{Name: "t.kern", Bytes: []byte(src)}})
		if len(prog.Diags) > 0 {
			t.Errorf("std/%s: %s", name, prog.Diags[0].Message)
			continue
		}
		if ds := check.Check(prog); len(ds) > 0 {
			t.Errorf("std/%s: %s at %s %v", name, ds[0].Message, ds[0].File, ds[0].Span)
		}
	}
}
