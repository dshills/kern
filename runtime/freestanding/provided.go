package freestanding

import (
	"regexp"
	"strings"
)

// Provided is the set of external symbols the freestanding runtime defines:
// the non-static file-scope functions and objects of rt.c and the global
// labels of the startup code (specs/freestanding-availability §1). The
// compiler reports a program that needs any other runtime entry before it
// links (Rule 6.8).
func Provided() map[string]bool {
	out := map[string]bool{}
	for name := range cDefinitions(RuntimeC) {
		out[name] = true
	}
	for _, s := range []string{StartAArch64, StartX8664} {
		for name := range asmGlobals(s) {
			out[name] = true
		}
	}
	return out
}

var (
	cIdent   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	asmGlobl = regexp.MustCompile(`^\s*\.(?:globl|global)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	asmLabel = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*):`)
)

// cDefinitions reads the external definitions of a C file written in the
// runtime's own style: a definition starts at column 0 and is neither
// static, extern, a typedef nor a preprocessor line; a function's
// declarator `type name(params)` is followed by `{` on its line or the
// next (a prototype, ending in `;`, defines nothing); an object's is
// `type name` followed by `=`, `[` or `;` (a tentative definition counts).
// Preprocessor conditions are not evaluated, so the result is the union
// over every target the file builds for.
func cDefinitions(src string) map[string]bool {
	out := map[string]bool{}
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if l == "" || l[0] == ' ' || l[0] == '\t' || l[0] == '#' || l[0] == '}' || l[0] == '{' || strings.HasPrefix(l, "//") || strings.HasPrefix(l, "/*") || strings.HasPrefix(l, " *") {
			continue
		}
		first := cIdent.FindString(l)
		if first == "static" || first == "extern" || first == "typedef" {
			continue
		}
		if p := strings.IndexByte(l, '('); p >= 0 {
			// a function: the name is the last identifier before `(`
			names := cIdent.FindAllString(l[:p], -1)
			if len(names) < 2 {
				continue // no return type: not a declarator at file scope
			}
			rest := strings.TrimSpace(l[p:])
			body := strings.Contains(rest, "{")
			if !body && i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "{") {
				body = true
			}
			if body {
				out[names[len(names)-1]] = true
			}
			continue
		}
		// an object: `type name` up to `=`, `[` or `;`
		end := strings.IndexAny(l, "=[;")
		if end < 0 {
			continue
		}
		names := cIdent.FindAllString(l[:end], -1)
		if len(names) >= 2 {
			out[names[len(names)-1]] = true
		}
	}
	return out
}

// asmGlobals reads the names an assembly file both declares global
// (`.globl`/`.global`) and defines (a label `name:`).
func asmGlobals(src string) map[string]bool {
	global, label := map[string]bool{}, map[string]bool{}
	for l := range strings.SplitSeq(src, "\n") {
		if m := asmGlobl.FindStringSubmatch(l); m != nil {
			global[m[1]] = true
		}
		if m := asmLabel.FindStringSubmatch(l); m != nil {
			label[m[1]] = true
		}
	}
	out := map[string]bool{}
	for n := range global {
		if label[n] {
			out[n] = true
		}
	}
	return out
}
