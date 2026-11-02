package analysis

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// HasBudgets reports whether any function declared in files carries a
// budget block (spec §7.6). Callers pass the request's own files, never
// the standard library or the prelude, so that only a budget the request
// declares makes a command lower the program.
func HasBudgets(files []*ast.File) bool {
	for _, f := range files {
		for _, fn := range funcDecls(f) {
			if fn.decl.Budget != nil {
				return true
			}
		}
	}
	return false
}

// budgetLimit is the largest budget value (spec §7.6); the resolver has
// already rejected anything above it.
const budgetLimit = 1<<31 - 1

// budgetKind is what one budget item counts: the noun of its entries and
// the label of the related location each one gets.
type budgetKind struct{ one, many, related string }

var budgetKinds = map[string]budgetKind{
	"bounds_checks": {"runtime-checked bounds check", "runtime-checked bounds checks", "runtime-checked bounds check"},
	"copies":        {"copy", "copies", "copy"},
	"allocations":   {"allocation site", "allocation sites", "allocation site"},
}

// budgetListed is how many locations a BUDGET_EXCEEDED message lists before
// "and N more"; budgetSnippet is how many code points of each location's
// source text it quotes.
const (
	budgetListed  = 10
	budgetSnippet = 40
)

// Budgets evaluates the budget blocks of prog's functions against mod, the
// show module (the module show-bounds-checks, show-copies and
// show-allocations read, at the command's level and target), and returns
// one BUDGET_EXCEEDED per item a function exceeds, sorted (spec §7.6).
// prog must have resolved and checked with no error, so every item is
// valid. srcs supplies the source text the messages quote.
func Budgets(prog *resolve.Program, srcs []*source.File, mod *ir.Module, target string) []diag.Diagnostic {
	type budgeted struct {
		fn   namedFunc
		file string
	}
	var fns []budgeted
	for _, m := range prog.Modules {
		for _, f := range m.Files {
			for _, fn := range funcDecls(f) {
				if fn.decl.Budget != nil && len(fn.decl.Budget.Items) > 0 {
					fns = append(fns, budgeted{fn, f.Name})
				}
			}
		}
	}
	if len(fns) == 0 {
		return nil
	}
	entries := map[string][]diag.Loc{}
	for _, c := range BoundsChecks(mod).Checks {
		if c.Kind == diag.CheckBounds && c.Label == diag.LabelRuntimeChecked {
			entries["bounds_checks"] = append(entries["bounds_checks"], c.Loc)
		}
	}
	for _, c := range Copies(mod, target).Copies {
		entries["copies"] = append(entries["copies"], c.Loc)
	}
	for _, s := range Allocations(mod).Sites {
		entries["allocations"] = append(entries["allocations"], s.Loc)
	}
	text := map[string][]byte{}
	for _, s := range srcs {
		text[s.Name] = s.Bytes
	}
	var out []diag.Diagnostic
	for _, b := range fns {
		d := b.fn.decl
		for _, it := range d.Budget.Items {
			kind, ok := budgetKinds[it.Name]
			if !ok {
				continue // BUDGET_INVALID, reported by the resolver
			}
			limit, err := strconv.ParseUint(strings.ReplaceAll(it.Value, "_", ""), 10, 64)
			if err != nil || limit > budgetLimit {
				continue // likewise
			}
			var counted []diag.Loc
			for _, l := range entries[it.Name] {
				if l.File == b.file && l.Span.Start >= d.Start && l.Span.Start < d.End {
					counted = append(counted, l)
				}
			}
			if uint64(len(counted)) <= limit {
				continue
			}
			sort.SliceStable(counted, func(i, j int) bool {
				if counted[i].Span.Start != counted[j].Span.Start {
					return counted[i].Span.Start < counted[j].Span.Start
				}
				return counted[i].Span.End < counted[j].Span.End
			})
			over := counted[limit]
			dg := diag.New("BUDGET_EXCEEDED", b.file, over.Span, "%s", budgetMessage(b.fn.name, kind, limit, counted, text[b.file]))
			dg.Related = append(dg.Related, diag.Related{Label: "budget declared here", Start: it.Start, End: it.End})
			for i, l := range counted {
				if uint64(i) != limit {
					dg.Related = append(dg.Related, diag.Related{Label: kind.related, Start: l.Span.Start, End: l.Span.End})
				}
			}
			out = append(out, dg)
		}
	}
	diag.Sort(out)
	return out
}

// budgetMessage is the sentence of a BUDGET_EXCEEDED: the function, the
// count and the budget, then each counted location as line:column with
// its source text, since the plain-text rendering prints no related spans.
func budgetMessage(name string, kind budgetKind, limit uint64, counted []diag.Loc, src []byte) string {
	noun := kind.many
	if len(counted) == 1 {
		noun = kind.one
	}
	var b strings.Builder
	fmt.Fprintf(&b, "function `%s` has %d %s, budget %d: ", name, len(counted), noun, limit)
	for i, l := range counted {
		if i == budgetListed {
			fmt.Fprintf(&b, ", and %d more", len(counted)-budgetListed)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		line, col := lineCol(src, l.Span.Start)
		fmt.Fprintf(&b, "%d:%d `%s`", line, col, snippet(src, l.Span))
	}
	return b.String()
}

// lineCol is the 1-based line and column, in code points, of offset in src.
func lineCol(src []byte, offset int) (int, int) {
	if offset > len(src) {
		offset = len(src)
	}
	before := src[:offset]
	line := 1 + strings.Count(string(before), "\n")
	start := strings.LastIndexByte(string(before), '\n') + 1
	return line, 1 + utf8.RuneCount(before[start:])
}

// snippet is the source text of span with each run of whitespace replaced
// by one space and, past budgetSnippet code points, cut there and
// followed by "...".
func snippet(src []byte, span diag.Span) string {
	if span.Start < 0 || span.End > len(src) || span.Start > span.End {
		return ""
	}
	var b strings.Builder
	n := 0
	space := false
	for _, r := range string(src[span.Start:span.End]) {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && n > 0 {
			if n == budgetSnippet {
				return b.String() + "..."
			}
			b.WriteByte(' ')
			n++
		}
		space = false
		if n == budgetSnippet {
			return b.String() + "..."
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// namedFunc is a function declaration with the name diagnostics give it:
// `f` at top level, `T.f` for a method of an impl of T.
type namedFunc struct {
	name string
	decl *ast.FuncDecl
}

// funcDecls lists the function declarations of f, top level and in impls.
func funcDecls(f *ast.File) []namedFunc {
	var out []namedFunc
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			out = append(out, namedFunc{d.Name, d})
		case *ast.ImplDecl:
			owner := typeExprName(d.Target)
			for _, fn := range d.Funcs {
				out = append(out, namedFunc{owner + "." + fn.Name, fn})
			}
		}
	}
	return out
}

// typeExprName is the name of an impl's target type, without arguments.
func typeExprName(t ast.TypeExpr) string {
	switch t := t.(type) {
	case *ast.TypeName:
		return t.Name
	case *ast.PrimType:
		return t.Name
	}
	return "_"
}
