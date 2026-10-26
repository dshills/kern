// Package hosted embeds the kern hosted runtime (PLAN.md 2.2) so that `kern
// build` can compile and link it with every program.
package hosted

import _ "embed"

// Source is the C source of the hosted runtime.
//
//go:embed src/kern_rt.c
var Source string
