package main

import (
	"fmt"
	"io"

	"github.com/dshills/kern/compiler/lsp"
)

// lspCommand runs the language server on standard input and output
// (specs/language-server/SPEC.md §1): no arguments, log lines on
// standard error, the exit status of §3.5.
func lspCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		_, _ = fmt.Fprintln(stderr, "usage: kern lsp (no arguments; LSP on standard input and output)")
		return 2
	}
	return lsp.New(stdin, stdout, lsp.Options{Log: stderr}).Run()
}
