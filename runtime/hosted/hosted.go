// Package hosted embeds the kern hosted runtime (PLAN.md 2.2,
// specs/runtime) so that `kern build` can compile and link it with every
// program.
package hosted

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
)

// The runtime's sources: kern_rt.c, the one translation unit clang
// compiles, which includes kern_rt.h (the entries' prototypes) and then
// each unit.
//
//go:embed src/*.c src/*.h
var files embed.FS

// Main is the name of the translation unit in src.
const Main = "kern_rt.c"

// Source is the hosted runtime as one self-contained C source: Main with
// every `#include "file"` of a file in src replaced by that file's text,
// between #line directives that keep clang's diagnostics naming the unit.
// A build compiles this text, so the runtime-object cache, which is keyed
// on it, follows an edit to any unit or to the header.
var Source = amalgamate(Main)

// Files lists the runtime's source files by name.
func Files() map[string]string {
	out := map[string]string{}
	ents, err := files.ReadDir("src")
	if err != nil {
		panic(err)
	}
	for _, e := range ents {
		b, err := files.ReadFile("src/" + e.Name())
		if err != nil {
			panic(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

var localInclude = regexp.MustCompile(`^#include "([^"]+)"$`)

func amalgamate(name string) string {
	b, err := files.ReadFile("src/" + name)
	if err != nil {
		panic(fmt.Sprintf("hosted runtime: %v", err))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "#line 1 \"%s\"\n", name)
	for i, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if m := localInclude.FindStringSubmatch(line); m != nil {
			out.WriteString(amalgamate(m[1]))
			fmt.Fprintf(&out, "#line %d \"%s\"\n", i+2, name)
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}
