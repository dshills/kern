package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/source"
)

// lldbScript is the lldb Python command module: `kern-own` prints the
// ownership state of the places on the current source line, through
// `kern explain-ownership --line=N`.
//
//go:embed kern_lldb.py
var lldbScript string

// debugCommand implements `kern debug [build flags] files... [-- args]`: a
// DWARF build, then lldb on it with the `kern-own` command available
// (PLAN.md Phase 5). Without lldb the debug binary and the command file are
// left in place and their paths are printed.
func debugCommand(args []string, stdout, stderr io.Writer) int {
	opts, files, prog, err := parseRunArgs(args)
	if err != nil {
		return fail(stdout, "CLI_INVALID_FLAGS", "%v", err)
	}
	if len(files) == 0 {
		return fail(stdout, "API_NO_FILES", "debug names no files")
	}
	if opts.Env == "freestanding" {
		return fail(stdout, "CLI_INVALID_FLAGS", "debug runs hosted programs only (no --env=freestanding)")
	}
	var srcs []*source.File
	for _, name := range files {
		f, err := source.Load(name)
		if err != nil {
			return fail(stdout, "API_FILE_NOT_FOUND", "cannot read %s: %v", name, err)
		}
		srcs = append(srcs, f)
	}
	dir, err := os.MkdirTemp("", "kern-debug-")
	if err != nil {
		return internal(Request{Files: files}, &InternalError{Err: err}, stdout, stderr)
	}
	exe := filepath.Join(dir, "prog")
	ds, err := native.Build(srcs, exe, native.Options{Opt: opts.Opt, Debug: true, TestHooks: opts.TestHooks, ElideChecks: opts.ElideChecks})
	if err != nil {
		return internal(Request{Files: files}, &InternalError{Err: err}, stdout, stderr)
	}
	if len(ds) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	script := filepath.Join(dir, "kern_lldb.py")
	if err := os.WriteFile(script, []byte(lldbScript), 0o644); err != nil {
		return internal(Request{Files: files}, &InternalError{Err: err}, stdout, stderr)
	}
	self, _ := os.Executable()
	cmds := filepath.Join(dir, "kern.lldb")
	// the command file configures the module's environment itself, so it
	// works when lldb is started by hand with `-s`
	body := fmt.Sprintf("script import os; os.environ[\"KERN_BIN\"] = %s; os.environ[\"KERN_FILES\"] = %s\ncommand script import %q\n", pyString(self), pyString(joinFiles(files)), script)
	if err := os.WriteFile(cmds, []byte(body), 0o644); err != nil {
		return internal(Request{Files: files}, &InternalError{Err: err}, stdout, stderr)
	}
	lldb, err := exec.LookPath("lldb")
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "lldb not found; debug binary: %s\nlldb command file: %s\n", exe, cmds)
		return 0
	}
	lldbArgs := append([]string{"-s", cmds, "--", exe}, prog...)
	cmd := exec.Command(lldb, lldbArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "KERN_BIN="+self, "KERN_FILES="+joinFiles(files))
	if err := cmd.Run(); err != nil {
		return 1
	}
	return 0
}

// pyString renders s as a Python string literal (double-quoted, escaped).
func pyString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\x%02x", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func joinFiles(files []string) string {
	out := ""
	for i, f := range files {
		if i > 0 {
			out += string(filepath.ListSeparator)
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			abs = f
		}
		out += abs
	}
	return out
}
