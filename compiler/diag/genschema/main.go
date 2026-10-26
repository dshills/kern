// Command genschema renders tools/agent-api/schema.md from the machine-API
// types in compiler/diag/schema.go.
package main

import (
	"fmt"
	"os"

	"github.com/dshills/kern/compiler/diag"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genschema <output.md>")
		os.Exit(2)
	}
	if err := os.WriteFile(os.Args[1], []byte(diag.RenderSchemaMarkdown()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
