package analysis

import (
	"fmt"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// The reasons a bounds check is runtime-checked (specs/unproven-reasons
// §3), in the order the classification tries them.
const (
	ReasonGuardNotOnEveryPath = "guard_not_on_every_path"
	ReasonGuardTooWeak        = "guard_too_weak"
	ReasonOtherLength         = "other_length"
	ReasonTypeRange           = "type_range"
	ReasonRemainderByValue    = "remainder_by_value"
	ReasonComputedIndex       = "computed_index"
	ReasonNoGuard             = "no_guard"
	ReasonSliceHighBound      = "slice_high_bound"
	ReasonSliceLowBound       = "slice_low_bound"
)

// ReasonCodes lists every reason in §3's order.
var ReasonCodes = []string{ReasonGuardNotOnEveryPath, ReasonGuardTooWeak, ReasonOtherLength, ReasonTypeRange,
	ReasonRemainderByValue, ReasonComputedIndex, ReasonNoGuard, ReasonSliceHighBound, ReasonSliceLowBound}

// unlocatedHint is the hint of an entry whose span is no access of §2.
const unlocatedHint = "test that the index is below the length on every path to it"

// Reason is the classification of one unproven bounds entry: its §3 code
// and the hint that goes with it.
type Reason struct{ Code, Hint string }

// ClassifyStats is what one classification did: the entries it classified
// and how many of them it could not locate in the source (§2's fallback).
type ClassifyStats struct{ Entries, Unlocated int }

// Classifier classifies the unproven bounds entries of snap for which want
// returns true (every one when want is nil), returning a Reason per
// classified entry keyed by its index in snap.Result.Checks. prog must be
// the program the snapshot's module was lowered from, and srcs its sources.
type Classifier func(prog *resolve.Program, srcs []*source.File, snap *BoundsSnapshot, want func(diag.BoundsCheck) bool) (map[int]Reason, ClassifyStats)

var _ Classifier = ClassifyBounds

// ClassifyBounds is the Classifier of specs/unproven-reasons: each entry's
// reason comes from the source of the function that contains the access,
// its types, and whether the proof pass proved a slice's high bound;
// nothing else.
func ClassifyBounds(prog *resolve.Program, srcs []*source.File, snap *BoundsSnapshot, want func(diag.BoundsCheck) bool) (map[int]Reason, ClassifyStats) {
	text := map[string][]byte{}
	for _, s := range srcs {
		text[s.Name] = s.Bytes
	}
	files := map[string]*ast.File{}
	for _, m := range prog.Modules {
		for _, f := range m.Files {
			files[f.Name] = f
		}
	}
	out := map[int]Reason{}
	var st ClassifyStats
	decls := map[*ast.File][]namedFunc{}
	index := map[*ast.FuncDecl]*funcIndex{}
	for i, c := range snap.Result.Checks {
		if c.Kind != diag.CheckBounds || c.Label != diag.LabelRuntimeChecked || (want != nil && !want(c)) {
			continue
		}
		st.Entries++
		cl := &classifier{info: prog.Info, src: text[c.Loc.File], decls: decls, index: index}
		r, ok := cl.classify(files[c.Loc.File], c.Loc.Span, snap.highFailed[i])
		if !ok {
			st.Unlocated++
			r = Reason{ReasonNoGuard, unlocatedHint}
		}
		out[i] = r
	}
	return out, st
}

// classifier classifies the accesses of one file. decls and index are
// shared by every classifier of one classification, so that each file's
// declarations are listed once and each function is walked once however
// many entries it has.
type classifier struct {
	info  *resolve.Info
	src   []byte
	decls map[*ast.File][]namedFunc
	index map[*ast.FuncDecl]*funcIndex
}

// funcIndex is one walk of a function: its accesses by span, its closure
// literals, and its guard candidates with the closure body (scope) each
// belongs to.
type funcIndex struct {
	accesses map[[2]int]access
	closures []ast.Pos
	cands    []candidate
}

// access is the construct an entry's span covers (§2).
type access struct {
	start int
	x     ast.Expr // the place
	e     ast.Expr // an index access's index; nil for a slice
	hi    ast.Expr // a slice's high bound, nil when omitted
	slice bool
	// a byte-order call: e is the index argument I, and the width W gives
	// the synthetic index I + (W - 1)usize
	byteOrder bool
	width     int
}

// candidate is a guard candidate (§3): a comparison of a variable plus an
// offset against len(q).
type candidate struct {
	op      string
	lenLeft bool // len(q) is the left operand
	v       string
	a       int64
	q       ast.Expr
	start   int
	scope   ast.Pos // the innermost closure literal containing it, or {-1, -1}
}

func (c *classifier) classify(f *ast.File, span diag.Span, highFailed bool) (Reason, bool) {
	if f == nil || c.src == nil {
		return Reason{}, false
	}
	fns, ok := c.decls[f]
	if !ok {
		fns = funcDecls(f)
		c.decls[f] = fns
	}
	var decl *ast.FuncDecl
	for _, fn := range fns {
		if fn.decl.Start <= span.Start && span.Start < fn.decl.End {
			decl = fn.decl
			break
		}
	}
	if decl == nil {
		return Reason{}, false
	}
	fi, ok := c.index[decl]
	if !ok {
		fi = c.walk(decl)
		c.index[decl] = fi
	}
	acc, ok := fi.accesses[[2]int{span.Start, span.End}]
	if !ok {
		return Reason{}, false
	}
	x := c.text(acc.x)
	if acc.slice {
		if highFailed {
			h := "len(" + x + ")"
			if acc.hi != nil {
				h = c.text(acc.hi)
			}
			return Reason{ReasonSliceHighBound, fmt.Sprintf("the rules prove a high bound only when it is `len(%s)`, a literal within an array's length, or `v + c` with a literal c ≥ 1 under a guard `v + (c - 1) < len(%s)`; `%s <= len(%s)` does not prove it", x, x, h, x)}, true
		}
		return Reason{ReasonSliceLowBound, fmt.Sprintf("the rules prove a low bound only when it is `0usize`, a literal not above a literal high bound, `v + a` under a high bound `v + b` with a ≤ b, or below `len(%s)` under a guard when the high bound is `len(%s)`", x, x)}, true
	}
	e := c.text(acc.e)
	vNode, v, off, hasVar := variable(acc.e)
	if acc.byteOrder {
		switch {
		case acc.width == 0:
			hasVar = false // a width the types do not give (a type parameter): the index argument alone
		case acc.width > 1:
			e = clip(string(c.src[span0(acc.e).Start:span0(acc.e).End]) + fmt.Sprintf(" + %dusize", acc.width-1))
			off += int64(acc.width - 1)
		}
	}
	if hasVar {
		vText := c.text(vNode)
		var same, covering, other []candidate
		scope := fi.innermost(acc.start)
		for _, cd := range fi.cands {
			if cd.scope != scope || cd.v != v {
				continue
			}
			if c.samePlace(cd.q, acc.x) {
				same = append(same, cd)
				if covers(cd, off) {
					covering = append(covering, cd)
				}
			} else {
				other = append(other, cd)
			}
		}
		switch {
		case len(covering) > 0:
			lc := c.lineCol(cite(covering, acc.start).start)
			return Reason{ReasonGuardNotOnEveryPath, fmt.Sprintf("the guard at %s does not establish `%s < len(%s)` on every path to it (a path may bypass it or take its other branch, or a store to `%s`, a call that edits it or a write that may reach it may come after it): test `%s < len(%s)` just before it", lc, e, x, vText, e, x)}, true
		case len(same) > 0:
			lc := c.lineCol(cite(same, acc.start).start)
			return Reason{ReasonGuardTooWeak, fmt.Sprintf("the guard at %s does not bound `%s` below `len(%s)` as the rules read it: test `%s < len(%s)`", lc, e, x, e, x)}, true
		case len(other) > 0:
			cd := cite(other, acc.start)
			return Reason{ReasonOtherLength, fmt.Sprintf("the guard at %s bounds `%s` by `len(%s)`, not `len(%s)`: test `%s < len(%s)` just before it", c.lineCol(cd.start), vText, c.text(cd.q), x, e, x)}, true
		}
		return Reason{ReasonNoGuard, fmt.Sprintf("test `%s < len(%s)` on every path to it", e, x)}, true
	}
	if !acc.byteOrder {
		if t, u, m, ok := c.typeRange(acc); ok {
			return Reason{ReasonTypeRange, fmt.Sprintf("the range of `%s` is not a bound the rules use: mask the index, `%s & %d%s`", t, e, m, u)}, true
		}
		if b, ok := unparen(acc.e).(*ast.Binary); ok && b.Op == "%" {
			if _, lit := unparen(b.R).(*ast.IntLit); !lit {
				return Reason{ReasonRemainderByValue, fmt.Sprintf("a remainder by a value is not a bound the rules use: put `%s` in a local and test it below `len(%s)` just before the access", e, x)}, true
			}
		}
	}
	if _, lit := unparen(acc.e).(*ast.IntLit); !lit {
		return Reason{ReasonComputedIndex, fmt.Sprintf("no guard establishes `%s < len(%s)` on every path to it, and the rules do not derive it from guards on the parts of `%s`: put `%s` in a local and test it below `len(%s)` just before the access", e, x, e, e, x)}, true
	}
	return Reason{ReasonNoGuard, fmt.Sprintf("test `%s < len(%s)` on every path to it", e, x)}, true
}

// walk indexes decl in one traversal: every access of §2 by its span,
// every closure literal, and every guard candidate of §3 with its scope.
func (c *classifier) walk(decl *ast.FuncDecl) *funcIndex {
	fi := &funcIndex{accesses: map[[2]int]access{}}
	ast.Walk(decl, func(n any) bool {
		switch x := n.(type) {
		case *ast.IndexExpr:
			if len(x.Args) == 1 {
				fi.accesses[[2]int{x.Start, x.End}] = access{start: x.Start, x: unparen(x.X), e: unparen(x.Args[0])}
			}
		case *ast.SliceExpr:
			fi.accesses[[2]int{x.Start, x.End}] = access{start: x.Start, x: unparen(x.X), hi: unparenOrNil(x.Hi), slice: true}
		case *ast.CallExpr:
			if byteOrderCall(x) && len(x.Args) >= 2 {
				fi.accesses[[2]int{x.Start, x.End}] = access{start: x.Start, x: unparen(x.Args[0].X), e: unparen(x.Args[1].X), byteOrder: true, width: c.byteWidth(x)}
			}
		case *ast.ClosureExpr:
			fi.closures = append(fi.closures, x.Pos)
		case *ast.If:
			fi.cond(x.Cond)
		case *ast.While:
			fi.cond(x.Cond)
		case *ast.For:
			if r, ok := unparen(x.Iter).(*ast.RangeExpr); ok && x.Var2 == "" {
				if q := lenArg(r.Hi); q != nil {
					fi.cands = append(fi.cands, candidate{op: "<", v: x.Var, q: q, start: x.Start})
				}
			}
		}
		return false
	})
	for i := range fi.cands {
		fi.cands[i].scope = fi.innermost(fi.cands[i].start)
	}
	return fi
}

// cond adds the candidates of a condition: its comparisons against a
// length, through &&, || and !.
func (fi *funcIndex) cond(e ast.Expr) {
	switch x := unparen(e).(type) {
	case *ast.Binary:
		switch x.Op {
		case "&&", "||":
			fi.cond(x.L)
			fi.cond(x.R)
		case "<", "<=", ">", ">=":
			other, q, lenLeft := x.R, lenArg(x.L), true
			if q == nil {
				other, q, lenLeft = x.L, lenArg(x.R), false
			}
			if q == nil {
				return
			}
			if _, v, a, ok := variable(other); ok {
				fi.cands = append(fi.cands, candidate{op: x.Op, lenLeft: lenLeft, v: v, a: a, q: q, start: x.Start})
			}
		}
	case *ast.Unary:
		if x.Op == "!" {
			fi.cond(x.X)
		}
	}
}

// innermost is the smallest closure literal containing offset at, or
// {-1, -1} when it is in the function's own body.
func (fi *funcIndex) innermost(at int) ast.Pos {
	best := ast.Pos{Start: -1, End: -1}
	for _, p := range fi.closures {
		if p.Start <= at && at < p.End && (best.Start < 0 || p.End-p.Start < best.End-best.Start) {
			best = p
		}
	}
	return best
}

// byteOrderCall reports a call of load_be, load_le, store_be or store_le,
// with or without its type argument.
func byteOrderCall(x *ast.CallExpr) bool {
	fn := x.Fn
	if ix, ok := fn.(*ast.IndexExpr); ok {
		fn = ix.X
	}
	id, ok := fn.(*ast.Ident)
	return ok && (id.Name == "load_be" || id.Name == "load_le" || id.Name == "store_be" || id.Name == "store_le")
}

// byteWidth is the width in bytes of the integer a byte-order call reads
// (its type) or writes (its value argument's), or 0 when the types do not
// give one.
func (c *classifier) byteWidth(x *ast.CallExpr) int {
	fn := x.Fn
	if ix, ok := fn.(*ast.IndexExpr); ok {
		fn = ix.X
	}
	var t resolve.Type
	if id, ok := fn.(*ast.Ident); ok && (id.Name == "load_be" || id.Name == "load_le") {
		t = c.info.Types[x]
	} else if len(x.Args) >= 3 {
		t = c.info.Types[x.Args[2].X]
	}
	if p, ok := t.(*resolve.Prim); ok {
		return intBytes[p.Name]
	}
	return 0
}

var intBytes = map[string]int{"u8": 1, "i8": 1, "u16": 2, "i16": 2, "u32": 4, "i32": 4, "u64": 8, "i64": 8, "usize": 8, "isize": 8}

// unsignedBits are the widths of the unsigned integer types.
var unsignedBits = map[string]int{"u8": 8, "u16": 16, "u32": 32, "u64": 64, "usize": 64}

// typeRange matches §3's type_range: the index is a conversion U(e) of a
// narrower unsigned e (a checked conversion, not truncate[U]) into an
// array of at least 2^b elements, with no type
// parameter in the three types. It returns e's type, U and the mask.
func (c *classifier) typeRange(acc access) (string, string, uint64, bool) {
	conv, ok := unparen(acc.e).(*ast.ConvExpr)
	if !ok || conv.Truncate {
		return "", "", 0, false
	}
	u, ok := c.info.Types[conv].(*resolve.Prim)
	if !ok || u.Name != conv.To {
		return "", "", 0, false
	}
	t, ok := c.info.Types[conv.X].(*resolve.Prim)
	if !ok {
		return "", "", 0, false
	}
	ub, uok := unsignedBits[u.Name]
	b, tok := unsignedBits[t.Name]
	if !uok || !tok || b >= ub {
		return "", "", 0, false
	}
	xt := c.info.Types[acc.x]
	if a, ok := xt.(*resolve.Access); ok {
		xt = a.Elem
	}
	arr, ok := xt.(*resolve.Array)
	if !ok || hasTypeParam(arr) || arr.Len < 0 || uint64(arr.Len) < uint64(1)<<b {
		return "", "", 0, false
	}
	return t.Name, u.Name, uint64(1)<<b - 1, true
}

// hasTypeParam reports a type parameter anywhere in t.
func hasTypeParam(t resolve.Type) bool {
	switch t := t.(type) {
	case *resolve.TypeParamType:
		return true
	case *resolve.Array:
		return hasTypeParam(t.Elem)
	case *resolve.Slice:
		return hasTypeParam(t.Elem)
	case *resolve.Access:
		return hasTypeParam(t.Elem)
	case *resolve.RawPtr:
		return hasTypeParam(t.Elem)
	case *resolve.Named:
		for _, a := range t.Args {
			if hasTypeParam(a) {
				return true
			}
		}
	case *resolve.Tuple:
		for _, e := range t.Elems {
			if hasTypeParam(e) {
				return true
			}
		}
	}
	return false
}

// covers reports that a candidate covers an index with offset o (§3):
// a strict comparison `V + a < len(Q)` (or the forms whose false edge gives
// it) when a >= o, a non-strict one when a >= 1 and a - 1 >= o.
func covers(cd candidate, o int64) bool {
	strict := (cd.op == "<" || cd.op == ">=") && !cd.lenLeft || (cd.op == ">" || cd.op == "<=") && cd.lenLeft
	if strict {
		return cd.a >= o
	}
	return cd.a >= 1 && cd.a-1 >= o
}

// cite is the candidate a hint names: the last one starting before the
// access, else the first one after it.
func cite(cs []candidate, at int) candidate {
	best := -1
	for i, cd := range cs {
		if cd.start < at {
			best = i
		}
	}
	if best < 0 {
		best = 0
	}
	return cs[best]
}

// variable is an index's variable (§2): a name or a field path of a name,
// optionally plus or minus an integer literal. It returns the path node,
// its key, the offset, and whether the index has one.
func variable(e ast.Expr) (ast.Expr, string, int64, bool) {
	e = unparen(e)
	if b, ok := e.(*ast.Binary); ok && (b.Op == "+" || b.Op == "-") {
		lit, ok := unparen(b.R).(*ast.IntLit)
		if !ok || lit.Value > 1<<62 {
			return nil, "", 0, false
		}
		node := unparen(b.L)
		key, ok := path(node)
		if !ok {
			return nil, "", 0, false
		}
		off := int64(lit.Value)
		if lit.Neg != (b.Op == "-") {
			off = -off
		}
		return node, key, off, true
	}
	key, ok := path(e)
	if !ok {
		return nil, "", 0, false
	}
	return e, key, 0, true
}

// path is the key of a name or a field path of a name.
func path(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name, true
	case *ast.FieldExpr:
		if p, ok := path(x.X); ok {
			return p + "." + x.Name, true
		}
	}
	return "", false
}

// lenArg is Q when e is `len(Q)`, without its parentheses, else nil.
func lenArg(e ast.Expr) ast.Expr {
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return nil
	}
	if id, ok := call.Fn.(*ast.Ident); !ok || id.Name != "len" {
		return nil
	}
	return unparen(call.Args[0].X)
}

// samePlace compares two places' token sequences (§3).
func (c *classifier) samePlace(a, b ast.Expr) bool {
	ta, tb := c.tokens(a), c.tokens(b)
	if ta == nil || len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i].Kind != tb[i].Kind || ta[i].Text != tb[i].Text {
			return false
		}
	}
	return true
}

func (c *classifier) tokens(e ast.Expr) []lexer.Token {
	p := span0(e)
	if p.Start < 0 || p.End > len(c.src) || p.Start > p.End {
		return nil
	}
	toks, _ := lexer.Lex(c.src[p.Start:p.End])
	return toks
}

// text is a fragment of a hint: e's source text, clipped (§0's glossary).
func (c *classifier) text(e ast.Expr) string {
	p := span0(e)
	return snippet(c.src, diag.Span{Start: p.Start, End: p.End})
}

func (c *classifier) lineCol(offset int) string {
	l, col := lineCol(c.src, offset)
	return fmt.Sprintf("%d:%d", l, col)
}

// span0 is a node's span.
func span0(e ast.Expr) ast.Pos {
	if s, ok := e.(interface{ Span() ast.Pos }); ok {
		return s.Span()
	}
	return ast.Pos{Start: -1, End: -1}
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.Paren)
		if !ok {
			return e
		}
		e = p.X
	}
}

func unparenOrNil(e ast.Expr) ast.Expr {
	if e == nil {
		return nil
	}
	return unparen(e)
}
