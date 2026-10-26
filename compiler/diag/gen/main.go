// Command gen renders the diagnostic registry as spec/rules.md.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/diag"
)

func main() {
	var codes []string
	for c := range diag.Registry {
		codes = append(codes, string(c))
	}
	sort.Strings(codes)
	var sb strings.Builder
	sb.WriteString("# kern diagnostic codes\n\nGenerated from `compiler/diag/codes.go` by `go generate ./compiler/diag`; do not edit.\nRule identifiers are `section.bullet` into the specification (`T.n` = spec/types.md §n, `P.0` = PLAN.md §0).\n\n| Code | Rule | Category | Phase | Summary |\n|---|---|---|---|---|\n")
	for _, c := range codes {
		info := diag.Registry[diag.Code(c)]
		fmt.Fprintf(&sb, "| `%s` | %s | %s | %d | %s |\n", c, info.Rule, info.Category, info.Phase, info.Summary)
	}
	if err := os.WriteFile(os.Args[1], []byte(sb.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
