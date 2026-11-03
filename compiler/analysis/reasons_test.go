package analysis_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// classified is a program's unproven bounds entries, keyed by their source
// text, with the classification of specs/unproven-reasons.
type classified struct {
	src     string
	reasons map[string]analysis.Reason
	stats   analysis.ClassifyStats
}

func classify(t *testing.T, src, level string) classified {
	t.Helper()
	p := buildBudgeted(t, map[string]string{"m.kern": src}, level)
	snap := analysis.Snapshot(p.mod)
	rs, st := analysis.ClassifyBounds(p.prog, p.srcs, snap, nil)
	out := classified{src: src, reasons: map[string]analysis.Reason{}, stats: st}
	for i, r := range rs {
		c := snap.Result.Checks[i]
		key := src[c.Loc.Span.Start:c.Loc.Span.End]
		if _, dup := out.reasons[key]; dup {
			t.Fatalf("two unproven entries quote %q; make the fixture's accesses distinct", key)
		}
		out.reasons[key] = r
	}
	if st.Unlocated != 0 {
		t.Errorf("%d entries not located", st.Unlocated)
	}
	return out
}

// at is the line:column of the first occurrence of s in the source.
func (c classified) at(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(c.src, s)
	if i < 0 || strings.Count(c.src, s) != 1 {
		t.Fatalf("%q occurs %d times in the fixture", s, strings.Count(c.src, s))
	}
	line := 1 + strings.Count(c.src[:i], "\n")
	col := i - strings.LastIndexByte(c.src[:i], '\n')
	return fmt.Sprintf("%d:%d", line, col)
}

func (c classified) expect(t *testing.T, access, code, hint string) {
	t.Helper()
	r, ok := c.reasons[access]
	if !ok {
		t.Errorf("%s: not an unproven entry (entries: %v)", access, keys(c.reasons))
		return
	}
	if r.Code != code || r.Hint != hint {
		t.Errorf("%s:\n got %s: %s\nwant %s: %s", access, r.Code, r.Hint, code, hint)
	}
}

func keys(m map[string]analysis.Reason) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// unprovenIn counts the unproven bounds entries inside function name.
func unprovenIn(t *testing.T, src, level, name string) int {
	t.Helper()
	p := buildBudgeted(t, map[string]string{"m.kern": src}, level)
	start := strings.Index(src, "func "+name+"(")
	if start < 0 {
		t.Fatalf("no function %s", name)
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		end = len(src)
	} else {
		end += start + 1
	}
	n := 0
	for _, c := range analysis.BoundsChecks(p.mod).Checks {
		if c.Kind == diag.CheckBounds && c.Label == diag.LabelRuntimeChecked && c.Loc.Span.Start >= start && c.Loc.Span.Start < end {
			n++
		}
	}
	return n
}

const reasonsSource = `module m
func count(xs: view[]u64) -> usize { return len(xs) }
func f1(xs: view[]u64, i: usize, c: bool) -> u64 {
    if c {
        if i >= len(xs) { return 0u64 }
    }
    return xs[i]
}
func f2(xs: view[]u64, i: usize) -> u64 {
    if i + 1usize <= len(xs) { return xs[i + 1usize] }
    return 0u64
}
func f4(data: view[]u8, counts: edit[[256]u64]) {
    for b in data { counts[usize(b)] = 1u64 }
}
func f5(src: view[]u64, i: usize) -> u64 {
    if len(src) == 0usize { return 0u64 }
    return src[i % len(src)]
}
func f6(s: view[]u64, n: usize, i: usize) -> u64 { return s[n - 1usize - i] }
func f7(xs: view[]u64, mid: usize) -> u64 { return xs[mid] }
func f7lit(ps: view[]u64) -> u64 { return ps[0usize] }
func f1b(xs: view[]u64, n: usize, c: bool) -> u64 {
    if c {
        if n > len(xs) { return 0u64 }
    }
    return xs[n - 1usize]
}
func f1c(xs: view[]u64, n: usize) -> u64 {
    if n <= len(xs) { return xs[n] }
    return 0u64
}
func f8(h: view[]u64, n: usize) -> usize { return count(h[0usize:n]) }
func f8b(g: view[]u64, i: usize, n: usize) -> usize { return count(g[i:n]) }
func f8c(g: view[]u64, xs: view[]u64, w: usize) -> usize {
    if count(xs) + w <= len(g) { return count(g[count(xs):count(xs) + w]) }
    return 0usize
}
func f8d(h: view[]u64, n: usize) -> usize {
    if n <= len(h) { return count(h[0usize:n - 1usize]) }
    return 0usize
}
func f8e(h: view[]u64, s: usize, n: usize) -> usize {
    k := n - 1usize
    return count(h[s:k])
}
func f8f(h: view[]u64, xs: view[]u64, n: usize) -> usize {
    if n <= len(h) { return count(h[count(xs):n - 1usize]) }
    return 0usize
}
func f9(a: view[]u64, s: usize) -> usize { return count(a[s:len(a)]) }
func f9b(a: view[]u64, s: usize, e: usize) -> usize {
    if e <= len(a) { return count(a[s:e]) }
    return 0usize
}
func f9c(g: view[]u64, i: usize, w: usize) -> usize {
    if i + w <= len(g) { return count(g[i:i + w]) }
    return 0usize
}
func main() -> i32 { return 0i32 }
`

// reasonsRewritten is reasonsSource with every access rewritten as its
// hint says.
const reasonsRewritten = `module m
func f1(xs: view[]u64, i: usize, c: bool) -> u64 {
    if c {
        if i >= len(xs) { return 0u64 }
    }
    if i < len(xs) { return xs[i] }
    return 0u64
}
func f2(xs: view[]u64, i: usize) -> u64 {
    if i + 1usize < len(xs) { return xs[i + 1usize] }
    return 0u64
}
func f3(xs: view[]u64, ys: view[]u64, i: usize) -> u64 {
    if i < len(ys) { if i < len(xs) { return xs[i] } }
    return 0u64
}
func f4(data: view[]u8, counts: edit[[256]u64]) {
    for b in data { counts[usize(b) & 255usize] = 1u64 }
}
func f5(src: view[]u64, i: usize) -> u64 {
    if len(src) == 0usize { return 0u64 }
    k := i % len(src)
    if k < len(src) { return src[k] }
    return 0u64
}
func f6(s: view[]u64, n: usize, i: usize) -> u64 {
    j := n - 1usize - i
    if j < len(s) { return s[j] }
    return 0u64
}
func f7(xs: view[]u64, mid: usize) -> u64 {
    if mid < len(xs) { return xs[mid] }
    return 0u64
}
func f7lit(ps: view[]u64) -> u64 {
    if 0usize < len(ps) { return ps[0usize] }
    return 0u64
}
func f1b(xs: view[]u64, n: usize, c: bool) -> u64 {
    if c {
        if n > len(xs) { return 0u64 }
    }
    k := n - 1usize
    if k < len(xs) { return xs[k] }
    return 0u64
}
func f1c(xs: view[]u64, n: usize) -> u64 {
    if n < len(xs) { return xs[n] }
    return 0u64
}
func f8(h: view[]u64, n: usize) -> usize {
    if n <= len(h) { return count(h[0usize:n]) }
    return 0usize
}
func f8b(g: view[]u64, i: usize, n: usize) -> usize {
    if i <= n && n <= len(g) { return count(g[i:n]) }
    return 0usize
}
func f8c(g: view[]u64, xs: view[]u64, w: usize) -> usize {
    lo := count(xs)
    hi := lo + w
    if lo <= hi && hi <= len(g) { return count(g[lo:hi]) }
    return 0usize
}
func f8d(h: view[]u64, n: usize) -> u64 {
    var s: u64 = 0u64
    with r := h[0usize:len(h)] {
        for x in 0usize..n { if x < len(r) { s = s + r[x] } }
    }
    return s
}
func f8e(h: view[]u64, s: usize, n: usize) -> u64 {
    var t: u64 = 0u64
    if s < len(h) {
        with r := h[s:len(h)] {
            for x in 0usize..n { if x < len(r) { t = t + r[x] } }
        }
    }
    return t
}
func f8f(h: view[]u64, xs: view[]u64, n: usize) -> u64 {
    var t: u64 = 0u64
    k := count(xs)
    if k < len(h) {
        with r := h[k:len(h)] {
            for x in 0usize..n { if x < len(r) { t = t + r[x] } }
        }
    }
    return t
}
func f9(a: view[]u64, s: usize) -> usize {
    lo := s
    hi := len(a)
    if lo <= hi { return count(a[lo:hi]) }
    return 0usize
}
func f9b(a: view[]u64, s: usize, e: usize) -> usize {
    if s <= e && e <= len(a) { return count(a[s:e]) }
    return 0usize
}
func f9c(g: view[]u64, i: usize, w: usize) -> usize {
    lo := i
    hi := i + w
    if hi <= len(g) && lo <= hi { return count(g[lo:hi]) }
    return 0usize
}
func count(xs: view[]u64) -> usize { return len(xs) }
func main() -> i32 { return 0i32 }
`

// TestReasonsEach: one access per reason of §3 gets it with its exact hint,
// and the access rewritten as the hint says is proven (R8).
func TestReasonsEach(t *testing.T) {
	c := classify(t, reasonsSource, "debug")
	c.expect(t, "xs[i]", analysis.ReasonGuardNotOnEveryPath,
		"the guard at "+c.at(t, "i >= len(xs)")+" does not establish `i < len(xs)` on every path to it (a path may bypass it or take its other branch, or a store to `i`, a call that edits it or a write that may reach it may come after it): test `i < len(xs)` just before it")
	c.expect(t, "xs[i + 1usize]", analysis.ReasonGuardTooWeak,
		"the guard at "+c.at(t, "i + 1usize <= len(xs)")+" does not bound `i + 1usize` below `len(xs)` as the rules read it: test `i + 1usize < len(xs)`")
	c.expect(t, "counts[usize(b)]", analysis.ReasonTypeRange,
		"the range of `u8` is not a bound the rules use: mask the index, `usize(b) & 255usize`")
	c.expect(t, "src[i % len(src)]", analysis.ReasonRemainderByValue,
		"a remainder by a value is not a bound the rules use: put `i % len(src)` in a local and test it below `len(src)` just before the access")
	c.expect(t, "s[n - 1usize - i]", analysis.ReasonComputedIndex,
		"no guard establishes `n - 1usize - i < len(s)` on every path to it, and the rules do not derive it from guards on the parts of `n - 1usize - i`: put `n - 1usize - i` in a local and test it below `len(s)` just before the access")
	c.expect(t, "xs[mid]", analysis.ReasonNoGuard, "test `mid < len(xs)` on every path to it")
	c.expect(t, "ps[0usize]", analysis.ReasonNoGuard, "test `0usize < len(ps)` on every path to it")
	// specs/prove-nonstrict C1: a non-strict guard with offset 0 covers
	// `n - 1usize`, not `n`
	c.expect(t, "xs[n - 1usize]", analysis.ReasonGuardNotOnEveryPath,
		"the guard at "+c.at(t, "n > len(xs)")+" does not establish `n - 1usize < len(xs)` on every path to it (a path may bypass it or take its other branch, or a store to `n`, a call that edits it or a write that may reach it may come after it): test `n - 1usize < len(xs)` just before it")
	c.expect(t, "xs[n]", analysis.ReasonGuardTooWeak,
		"the guard at "+c.at(t, "n <= len(xs)")+" does not bound `n` below `len(xs)` as the rules read it: test `n < len(xs)`")
	// C2: a simple high bound with a low bound of 0, with a simple low
	// bound to test, and bounds that are not simple
	const lenRewrite = "the rules do not prove a high bound they read as a value minus a literal: slice to `len(h)` instead, "
	c.expect(t, "h[0usize:n]", analysis.ReasonSliceHighBound, "test `n <= len(h)` just before it")
	c.expect(t, "g[i:n]", analysis.ReasonSliceHighBound, "test `i <= n && n <= len(g)` just before it")
	c.expect(t, "g[count(xs):count(xs) + w]", analysis.ReasonSliceHighBound,
		"put the bounds in locals, test the low one `<=` the high one when the low one is not `0usize` and the high one `<= len(g)` just before the access, and slice with them")
	// C2 with HighNegative, directly and through a local
	c.expect(t, "h[0usize:n - 1usize]", analysis.ReasonSliceHighBound, lenRewrite+"`h[0usize:len(h)]`, and test each index below the slice's length")
	c.expect(t, "h[s:k]", analysis.ReasonSliceHighBound, lenRewrite+"`h[s:len(h)]`, after testing `s < len(h)`, and test each index below the slice's length")
	c.expect(t, "h[count(xs):n - 1usize]", analysis.ReasonSliceHighBound, lenRewrite+"from `count(xs)` put in a local and tested below `len(h)` first, and test each index below the slice's length")
	// C3: bounds that are not both simple, and simple ones
	c.expect(t, "a[s:len(a)]", analysis.ReasonSliceLowBound,
		"put `s` and `len(a)` in locals, test the first `<=` the second just before the access, and slice with them")
	c.expect(t, "a[s:e]", analysis.ReasonSliceLowBound, "test `s <= e` just before it")
	c.expect(t, "g[i:i + w]", analysis.ReasonSliceLowBound,
		"put `i` and `i + w` in locals, test the first `<=` the second just before the access, and slice with them")
	for _, fn := range []string{"f1", "f1b", "f1c", "f2", "f3", "f4", "f5", "f6", "f7", "f7lit", "f8", "f8b", "f8c", "f8d", "f8e", "f8f", "f9", "f9b", "f9c"} {
		if n := unprovenIn(t, reasonsRewritten, "debug", fn); n != 0 {
			t.Errorf("rewritten %s: %d runtime-checked bounds checks, want 0", fn, n)
		}
	}
}

// TestReasonsOtherLength: a guard on another place's length (its rewrite
// is f3 of reasonsRewritten).
func TestReasonsOtherLength(t *testing.T) {
	c := classify(t, `module m
func f3(xs: view[]u64, ys: view[]u64, i: usize) -> u64 {
    if i < len(ys) { return xs[i] }
    return 0u64
}
func main() -> i32 { return 0i32 }
`, "debug")
	c.expect(t, "xs[i]", analysis.ReasonOtherLength,
		"the guard at "+c.at(t, "i < len(ys)")+" bounds `i` by `len(ys)`, not `len(xs)`: test `i < len(xs)` just before it")
}

// TestReasonsByteOrder: the byte-order calls' synthetic index (§2).
func TestReasonsByteOrder(t *testing.T) {
	long := strings.Repeat("a", 30) + " + " + strings.Repeat("b", 12)
	src := `module m
func w8(buf: view[]u8, i: usize) -> u64 { return load_be[u64](buf, i) }
func w1(buf: view[]u8, j: usize) -> u8 { return load_le[u8](buf, j) }
func onpath(buf: view[]u8, i: usize, c: bool) -> u64 {
    if c {
        if i + 8usize >= len(buf) { return 0u64 }
    }
    return load_le[u64](buf, i + 1usize)
}
func weak(buf: view[]u8, k: usize) -> u64 {
    if k + 7usize < len(buf) { return load_be[u64](buf, k + 1usize) }
    return 0u64
}
func long(buf: view[]u8, ` + strings.Repeat("a", 30) + `: usize, ` + strings.Repeat("b", 12) + `: usize) -> u64 { return load_be[u64](buf, ` + long + `) }
func main() -> i32 { return 0i32 }
`
	c := classify(t, src, "debug")
	c.expect(t, "load_be[u64](buf, i)", analysis.ReasonNoGuard, "test `i + 7usize < len(buf)` on every path to it")
	c.expect(t, "load_le[u8](buf, j)", analysis.ReasonNoGuard, "test `j < len(buf)` on every path to it")
	c.expect(t, "load_le[u64](buf, i + 1usize)", analysis.ReasonGuardNotOnEveryPath,
		"the guard at "+c.at(t, "i + 8usize >= len(buf)")+" does not establish `i + 1usize + 7usize < len(buf)` on every path to it (a path may bypass it or take its other branch, or a store to `i`, a call that edits it or a write that may reach it may come after it): test `i + 1usize + 7usize < len(buf)` just before it")
	c.expect(t, "load_be[u64](buf, k + 1usize)", analysis.ReasonGuardTooWeak,
		"the guard at "+c.at(t, "k + 7usize < len(buf)")+" does not bound `k + 1usize + 7usize` below `len(buf)` as the rules read it: test `k + 1usize + 7usize < len(buf)`")
	e := []rune(long + " + 7usize")
	cut := string(e[:40]) + "..."
	c.expect(t, "load_be[u64](buf, "+long+")", analysis.ReasonComputedIndex,
		fmt.Sprintf("no guard establishes `%s < len(buf)` on every path to it, and the rules do not derive it from guards on the parts of `%s`: put `%s` in a local and test it below `len(buf)` just before the access", cut, cut, cut))
}

// TestReasonsCandidates: which candidate a hint cites, closures, for
// headers, an inverted guard and a parenthesized length (§3).
func TestReasonsCandidates(t *testing.T) {
	src := `module m
func callf(f: closure(usize) -> u64, i: usize) -> u64 { return f(i) }
func before(xs: view[]u64, i: usize, c: bool) -> u64 {
    if c && i < len(xs) { return 1u64 }
    if c || i + 0usize < len(xs) { return 2u64 }
    v := xs[i]
    if i - 0usize < len(xs) { return v }
    return 0u64
}
func after(ys: view[]u64, i: usize) -> u64 {
    v := ys[i]
    if i < len(ys) { return v }
    if i < len(ys) && i > 3usize { return 0u64 }
    return 1u64
}
func inner(zs: view[]u64, i: usize, c: bool) -> u64 {
    if i < len(zs) { return 1u64 }
    return callf(|k| {
        if c { if i >= len(zs) { return 0u64 } }
        return zs[i] + u64(k)
    }, i)
}
func nested(ws: view[]u64, i: usize) -> u64 {
    x := callf(|k| { if i < len(ws) { return 1u64 }; return u64(k) }, i)
    return x + ws[i]
}
func header(as: view[]u64, bs: view[]u64) -> u64 {
    var s: u64 = 0u64
    for j in 0usize..len(bs) { s = s + as[j] }
    return s
}
func inverted(cs: view[]u64, i: usize) -> u64 {
    if i >= len(cs) { return cs[i] }
    return 0u64
}
func parens(ds: view[]u64, i: usize, c: bool) -> u64 {
    if c { if i >= len((ds)) { return 0u64 } }
    return ds[i]
}
func main() -> i32 { return 0i32 }
`
	c := classify(t, src, "debug")
	c.expect(t, "xs[i]", analysis.ReasonGuardNotOnEveryPath, guardNotOnEveryPath(c.at(t, "i + 0usize < len(xs)"), "i", "xs"))
	c.expect(t, "ys[i]", analysis.ReasonGuardNotOnEveryPath, guardNotOnEveryPath(c.at(t, "i < len(ys) { return v }"), "i", "ys"))
	c.expect(t, "zs[i]", analysis.ReasonGuardNotOnEveryPath, guardNotOnEveryPath(c.at(t, "i >= len(zs)"), "i", "zs"))
	c.expect(t, "ws[i]", analysis.ReasonNoGuard, "test `i < len(ws)` on every path to it")
	c.expect(t, "as[j]", analysis.ReasonOtherLength,
		"the guard at "+c.at(t, "for j in")+" bounds `j` by `len(bs)`, not `len(as)`: test `j < len(as)` just before it")
	c.expect(t, "cs[i]", analysis.ReasonGuardNotOnEveryPath, guardNotOnEveryPath(c.at(t, "i >= len(cs)"), "i", "cs"))
	c.expect(t, "ds[i]", analysis.ReasonGuardNotOnEveryPath, guardNotOnEveryPath(c.at(t, "i >= len((ds))"), "i", "ds"))
}

func guardNotOnEveryPath(at, e, x string) string {
	return fmt.Sprintf("the guard at %s does not establish `%s < len(%s)` on every path to it (a path may bypass it or take its other branch, or a store to `i`, a call that edits it or a write that may reach it may come after it): test `%s < len(%s)` just before it", at, e, x, e, x)
}

// TestReasonsCoveringForms: each written form of §3 at its boundary.
func TestReasonsCoveringForms(t *testing.T) {
	cases := []struct {
		guard string
		off   int // the largest offset the guard covers; -1 for none
	}{
		{"i + 1usize < len(xs)", 1}, {"len(xs) > i + 1usize", 1}, {"i + 1usize >= len(xs)", 1}, {"len(xs) <= i + 1usize", 1},
		{"i + 1usize <= len(xs)", 0}, {"len(xs) >= i + 1usize", 0}, {"i + 1usize > len(xs)", 0}, {"len(xs) < i + 1usize", 0},
		{"i <= len(xs)", -1}, {"len(xs) >= i", -1}, {"i > len(xs)", -1}, {"len(xs) < i", -1},
	}
	for _, tc := range cases {
		for o := 0; o <= 2; o++ {
			e := "i"
			if o > 0 {
				e = fmt.Sprintf("i + %dusize", o)
			}
			src := fmt.Sprintf("module m\nfunc g(xs: view[]u64, i: usize, c: bool) -> u64 {\n    if c { if %s { return 0u64 } }\n    return xs[%s]\n}\nfunc main() -> i32 { return 0i32 }\n", tc.guard, e)
			c := classify(t, src, "debug")
			want := analysis.ReasonGuardTooWeak
			if o <= tc.off {
				want = analysis.ReasonGuardNotOnEveryPath
			}
			if r := c.reasons["xs["+e+"]"]; r.Code != want {
				t.Errorf("guard %q, index %s: %s, want %s", tc.guard, e, r.Code, want)
			}
		}
	}
}

// TestReasonsTruncation: a long index is cut at 40 code points inside the
// template, whose own text is never cut.
func TestReasonsTruncation(t *testing.T) {
	a, b := strings.Repeat("p", 25), strings.Repeat("q", 22)
	e := a + " + " + b // 50 code points
	c := classify(t, "module m\nfunc g(xs: view[]u64, "+a+": usize, "+b+": usize) -> u64 { return xs["+e+"] }\nfunc main() -> i32 { return 0i32 }\n", "debug")
	cut := e[:40] + "..."
	c.expect(t, "xs["+e+"]", analysis.ReasonComputedIndex,
		fmt.Sprintf("no guard establishes `%s < len(xs)` on every path to it, and the rules do not derive it from guards on the parts of `%s`: put `%s` in a local and test it below `len(xs)` just before the access", cut, cut, cut))
}

// TestReasonsAggregation: occurrences of one location that differ take
// §3's earliest reason, whatever their order; a generic place is never
// type_range (R1).
func TestReasonsAggregation(t *testing.T) {
	// release inlines part into main, so its slice's occurrences are the two
	// copies, each proven against its own constants: alone, the second
	// fails only its low bound
	for calls, want := range map[string]string{
		"part(a, 3usize, 2usize)":                           analysis.ReasonSliceLowBound,
		"part(a, 1usize, 9usize) + part(a, 3usize, 2usize)": analysis.ReasonSliceHighBound,
		"part(a, 3usize, 2usize) + part(a, 1usize, 9usize)": analysis.ReasonSliceHighBound,
	} {
		c := classify(t, `module m
func part(h: view[[4]u64], lo: usize, hi: usize) -> usize { with s := h[lo:hi] { return len(s) } }
func main() -> i32 {
    var a: [4]u64 = zero()
    return i32(`+calls+`)
}
`, "release")
		if r := c.reasons["h[lo:hi]"]; r.Code != want {
			t.Errorf("%s: %s, want %s", calls, r.Code, want)
		}
	}
	c := classify(t, `module m
func gx[T](t: edit[[256]T], b: u8, v: own T) { t[usize(b)] = v }
func main() -> i32 {
    var a: [256]u64 = zero()
    gx(a, 3u8, 1u64)
    return 0i32
}
`, "debug")
	if r := c.reasons["t[usize(b)]"]; r.Code != analysis.ReasonComputedIndex {
		t.Errorf("generic place: %s, want %s", r.Code, analysis.ReasonComputedIndex)
	}
}

// TestReasonsDeterministic: classifying again, or a two-file program
// built from its files in the other order, gives the same result.
func TestReasonsDeterministic(t *testing.T) {
	files := map[string]string{
		"a.kern": "module m\nfunc fa(xs: view[]u64, i: usize, c: bool) -> u64 {\n    if c { if i >= len(xs) { return 0u64 } }\n    return xs[i] + xs[i + 1usize]\n}\n",
		"b.kern": "module m\nfunc fb(s: view[]u64, n: usize, i: usize) -> u64 { return s[n - 1usize - i] + s[i % len(s)] }\nfunc main() -> i32 { return 0i32 }\n",
	}
	type at struct {
		file       string
		start, end int
	}
	byLoc := func(order []string) map[at]analysis.Reason {
		var srcs []*source.File
		for _, n := range order {
			srcs = append(srcs, &source.File{Name: n, Bytes: []byte(files[n])})
		}
		prog := resolve.ResolveFor(srcs, false)
		if len(prog.Diags) > 0 {
			t.Fatalf("resolve: %s", prog.Diags[0].Message)
		}
		if ds := check.Check(prog); len(ds) > 0 {
			t.Fatalf("check: %s", ds[0].Message)
		}
		mod, ds := lower.Lower(prog)
		if len(ds) > 0 {
			t.Fatalf("lower: %s", ds[0].Message)
		}
		if err := opt.Run(mod, opt.OptionsFor("debug")); err != nil {
			t.Fatal(err)
		}
		snap := analysis.Snapshot(mod)
		rs, st := analysis.ClassifyBounds(prog, srcs, snap, nil)
		again, _ := analysis.ClassifyBounds(prog, srcs, snap, nil)
		if !reflect.DeepEqual(rs, again) {
			t.Errorf("order %v: a second classification differs", order)
		}
		if st.Entries != 4 || st.Unlocated != 0 {
			t.Errorf("order %v: stats %+v, want 4 entries, all located", order, st)
		}
		out := map[at]analysis.Reason{}
		for i, r := range rs {
			l := snap.Result.Checks[i].Loc
			out[at{l.File, l.Span.Start, l.Span.End}] = r
		}
		return out
	}
	ab, ba := byLoc([]string{"a.kern", "b.kern"}), byLoc([]string{"b.kern", "a.kern"})
	if len(ab) != 4 || !reflect.DeepEqual(ab, ba) {
		t.Errorf("file order changes the classification:\n%v\n%v", ab, ba)
	}
	p := buildBudgeted(t, map[string]string{"m.kern": reasonsSource}, "debug")
	snap := analysis.Snapshot(p.mod)
	all, _ := analysis.ClassifyBounds(p.prog, p.srcs, snap, nil)
	picked, st := analysis.ClassifyBounds(p.prog, p.srcs, snap, func(c diag.BoundsCheck) bool {
		return strings.Contains(reasonsSource[c.Loc.Span.Start:c.Loc.Span.End], "xs[")
	})
	if st.Entries != len(picked) || st.Entries == 0 || st.Entries >= len(all) {
		t.Errorf("want selected %d of %d entries", st.Entries, len(all))
	}
	for i, r := range picked {
		if all[i] != r {
			t.Errorf("entry %d: selected %v, all %v", i, r, all[i])
		}
	}
}

// An entry whose occurrences differ in HighNegative gets the hint for a
// high bound read as a value minus a literal: release inlining copies
// cut's slice into main twice, read as `v.len() - 1` and as `v.len()`,
// and only that rewrite is proven for both (specs/prove-nonstrict C2).
func TestReasonsHighNegativeAggregated(t *testing.T) {
	for _, order := range []string{"cut(a, v.len() + 1usize) + cut(a, v.len())", "cut(a, v.len()) + cut(a, v.len() + 1usize)"} {
		src := `module m
func count(xs: view[]u64) -> usize { return len(xs) }
func cut(h: view[]u64, n: usize) -> usize { return count(h[0usize:n - 1usize]) }
func main() -> i32 {
    var a: [4]u64 = zero()
    v := Vec[u64].new()
    v.push(1u64)
    x := ` + order + `
    return i32(x)
}
`
		// the premise: release inlining made two copies of cut's slice in
		// main, one read with a negative offset and one without; without
		// it the entry has one occurrence and the test proves nothing
		p := buildBudgeted(t, map[string]string{"m.kern": src}, "release")
		at := strings.Index(src, "func cut(")
		end := at + strings.Index(src[at:], "\n")
		var neg []bool
		for _, f := range p.mod.Funcs {
			for _, blk := range f.Blocks {
				for _, in := range blk.Instrs {
					if cb, ok := in.(*ir.CheckBounds); ok && cb.Index == nil && !f.Inlined && cb.Pos.Start >= at && cb.Pos.Start < end {
						neg = append(neg, cb.HighNegative)
					}
				}
			}
		}
		if len(neg) != 2 || neg[0] == neg[1] {
			t.Fatalf("%s: emitted copies of cut's slice read negative %v, want two that differ", order, neg)
		}
		c := classify(t, src, "release")
		c.expect(t, "h[0usize:n - 1usize]", analysis.ReasonSliceHighBound,
			"the rules do not prove a high bound they read as a value minus a literal: slice to `len(h)` instead, `h[0usize:len(h)]`, and test each index below the slice's length")
	}
}
