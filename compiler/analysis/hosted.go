package analysis

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/backend/llvm"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/runtime/freestanding"
)

// HostedResult is the hosted-only evaluation of a freestanding program
// (specs/freestanding-availability F1, F2): the diagnostics, and for the
// tests and the report, the hosted-only entries each library function
// reaches and the union over every reaching call.
type HostedResult struct {
	Diagnostics []diag.Diagnostic
	Library     map[string][]string
	Entries     []string
}

// HostedOnly reports the calls of the program's own functions that reach
// a runtime entry the freestanding runtime does not provide. mod is the
// show module (no library inlining) at the command's level; the entries
// are the ones the LLVM emitter declares when it emits mod for a
// freestanding image on triple, and the provided set is the runtime's
// own (freestanding.Provided), so the result agrees with the linker.
func HostedOnly(mod *ir.Module, triple string) (HostedResult, error) {
	refs, errs := llvm.EmitRecorded(mod, llvm.Options{Triple: triple, Freestanding: true, StackCheck: true})
	if len(errs) > 0 {
		return HostedResult{}, fmt.Errorf("hosted-only evaluation: emitting: %w", errors.Join(errs...))
	}
	provided := freestanding.Provided()
	// the hosted-only entries each function requests directly, and per
	// instruction for the program's own functions
	direct := map[*ir.Func]map[string]bool{}
	type site struct {
		f  *ir.Func
		b  *ir.Block
		in ir.Instr
	}
	var own []struct {
		site
		sym string
	}
	for _, r := range refs {
		if provided[r.Symbol] || r.Func == nil {
			continue
		}
		if direct[r.Func] == nil {
			direct[r.Func] = map[string]bool{}
		}
		direct[r.Func][r.Symbol] = true
		if !r.Func.Library {
			own = append(own, struct {
				site
				sym string
			}{site{r.Func, r.Block, r.Instr}, r.Symbol})
		}
	}
	// what each library function reaches through library calls and
	// references: a fixed point, so recursion terminates
	reach := map[*ir.Func]map[string]bool{}
	for _, f := range mod.Funcs {
		if f.Library {
			reach[f] = map[string]bool{}
			for s := range direct[f] {
				reach[f][s] = true
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, f := range mod.Funcs {
			if !f.Library {
				continue
			}
			for _, g := range callees(mod, f) {
				if !g.Library {
					continue
				}
				for s := range reach[g] {
					if !reach[f][s] {
						reach[f][s] = true
						changed = true
					}
				}
			}
		}
	}
	// the reaching calls, grouped by span with the union of their entries
	type key struct {
		file       string
		start, end int
	}
	type found struct {
		name    string
		entries map[string]bool
	}
	at := map[key]*found{}
	var order []key
	add := func(f *ir.Func, b *ir.Block, pos ast.Pos, name string, entries map[string]bool) {
		if pos == (ast.Pos{}) {
			pos = f.Pos // a call the compiler generated: the function's span (F1)
		}
		file := f.File
		if b != nil {
			file = b.FileIn(f)
		}
		k := key{file, pos.Start, pos.End}
		g := at[k]
		if g == nil {
			g = &found{name: name, entries: map[string]bool{}}
			at[k] = g
			order = append(order, k)
		}
		for s := range entries {
			g.entries[s] = true
		}
	}
	for _, o := range own {
		add(o.f, o.b, posOf(o.in), nameOf(o.in), map[string]bool{o.sym: true})
	}
	for _, f := range mod.Funcs {
		if f.Library || f.Intrinsic {
			continue // an Inlined function's calls are the program's (F2)
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				g := calleeOf(mod, in)
				if g == nil || !g.Library || len(reach[g]) == 0 {
					continue
				}
				add(f, b, posOf(in), sourceName(g.Name), reach[g])
			}
		}
	}
	res := HostedResult{Library: map[string][]string{}}
	all := map[string]bool{}
	for f, r := range reach {
		if len(r) > 0 {
			res.Library[sourceName(f.Name)] = union(res.Library[sourceName(f.Name)], r)
		}
	}
	for _, k := range order {
		g := at[k]
		entries := sortedKeys(g.entries)
		for _, s := range entries {
			all[s] = true
		}
		msg := fmt.Sprintf("call to `%s` needs the hosted runtime (`%s`", g.name, entries[0])
		if len(entries) > 1 {
			msg += fmt.Sprintf(" and %d more", len(entries)-1)
		}
		msg += "), which a freestanding image does not have"
		res.Diagnostics = append(res.Diagnostics, diag.New("ENV_HOSTED_ONLY", k.file, diag.Span{Start: k.start, End: k.end}, "%s", msg))
	}
	diag.Sort(res.Diagnostics)
	res.Entries = sortedKeys(all)
	return res, nil
}

// callees are the module functions f calls or references by name.
func callees(mod *ir.Module, f *ir.Func) []*ir.Func {
	var out []*ir.Func
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if g := calleeOf(mod, in); g != nil {
				out = append(out, g)
			}
		}
	}
	return out
}

// calleeOf is the module function a call names or a function reference
// makes a value of; an intrinsic, an indirect call and any other
// instruction have none (a call through a function value adds no edge:
// the reference that made the value is one, F1).
func calleeOf(mod *ir.Module, in ir.Instr) *ir.Func {
	switch i := in.(type) {
	case *ir.Call:
		if i.Intrinsic == "" && i.Fn == nil && i.Callee != "" {
			return mod.Func(i.Callee)
		}
	case *ir.FuncRef:
		return mod.Func(i.Name)
	}
	return nil
}

// posOf is an instruction's source span, zero when it has none.
func posOf(in ir.Instr) ast.Pos {
	switch i := in.(type) {
	case *ir.Call:
		return i.Pos
	case *ir.FuncRef:
		return i.Pos
	case *ir.CheckBounds:
		return i.Pos
	case *ir.CheckOverflow:
		return i.Pos
	case *ir.Drop:
		return i.Pos
	}
	return ast.Pos{}
}

// nameOf is the source-level name of what an instruction of the program's
// own calls: an intrinsic's name, a callee's, or a release function's.
func nameOf(in ir.Instr) string {
	switch i := in.(type) {
	case *ir.Call:
		if i.Intrinsic != "" {
			return sourceName(i.Intrinsic)
		}
		if i.Callee != "" {
			return sourceName(i.Callee)
		}
	case *ir.AccessEnd:
		if i.Release != "" {
			return sourceName(i.Release)
		}
	case *ir.FuncRef:
		return sourceName(i.Name)
	}
	return "an operation"
}

// sourceName drops a function's type arguments and the prelude's module
// prefix, which source never writes: `Vec.push[T=u8]` is `Vec.push`, and
// `core.now_ns` is `now_ns`.
func sourceName(name string) string {
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	return strings.TrimPrefix(name, "core.")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func union(a []string, b map[string]bool) []string {
	m := map[string]bool{}
	for _, s := range a {
		m[s] = true
	}
	for s := range b {
		m[s] = true
	}
	return sortedKeys(m)
}
