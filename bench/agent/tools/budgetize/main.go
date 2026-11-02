// Command budgetize regenerates the budgeted agent suite,
// bench/agent/tasks-budgeted, from suite 4 (specs/declared-budgets §5).
// Run it from the repository root: go run ./bench/agent/tools/budgetize.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dshills/kern/bench/agent/budgetize"
)

func main() {
	from := flag.String("from", "bench/agent/tasks", "the suite to copy")
	to := flag.String("to", "bench/agent/tasks-budgeted", "the budgeted suite to write; its name must be "+budgetize.SuiteName)
	flag.Parse()
	if err := budgetize.Suite(*from, *to); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
