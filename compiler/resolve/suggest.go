package resolve

import "sort"

// nearest is the candidate a typo of name most plausibly meant: the closest
// by edit distance when that distance is one for a name of up to four
// characters or at most two otherwise, the lexically first among ties, and
// "" when nothing is close enough. The caller offers it as a fix.
func nearest(name string, cands []string) string {
	limit := 1
	if len(name) >= 5 {
		limit = 2
	}
	best, bestD := "", limit+1
	sort.Strings(cands)
	rn := []rune(name)
	for _, c := range cands {
		if c == name || c == "" {
			continue
		}
		rc := []rune(c)
		// a length difference is a lower bound on the distance
		if diff := len(rc) - len(rn); diff > limit || -diff > limit {
			continue
		}
		if d := editDistance(rn, rc, limit); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b when it is at
// most limit, and limit+1 otherwise. Only the cells within limit of the
// diagonal are computed: an alignment that leaves that band has already
// made more than limit insertions or deletions.
func editDistance(ra, rb []rune, limit int) int {
	if d := len(ra) - len(rb); d > limit || -d > limit {
		return limit + 1
	}
	far := limit + 1
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
		if j > limit {
			prev[j] = far
		}
	}
	for i := 1; i <= len(ra); i++ {
		lo, hi := max(1, i-limit), min(len(rb), i+limit)
		// only the cell before the band is read from this row, and only the
		// cell after it from this row as the next row's prev
		cur[lo-1] = far
		if lo == 1 && i <= limit {
			cur[0] = i
		}
		rowMin := far
		for j := lo; j <= hi; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			rowMin = min(rowMin, cur[j])
		}
		if hi < len(rb) {
			cur[hi+1] = far
		}
		if rowMin > limit {
			return far
		}
		prev, cur = cur, prev
	}
	return min(prev[len(rb)], far)
}

// keys lists a map's keys.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// visibleNames lists the names an identifier in sc could have meant: the
// locals of the enclosing scopes, the module's functions, constants,
// thread-locals, types and imports, and the prelude's functions.
func (c *fnCtx) visibleNames(sc *scope) []string {
	var out []string
	for s := sc; s != nil; s = s.parent {
		out = append(out, keys(s.vars)...)
	}
	out = append(out, keys(c.m.Funcs)...)
	out = append(out, keys(c.m.Consts)...)
	out = append(out, keys(c.m.Globals)...)
	out = append(out, keys(c.m.Types)...)
	out = append(out, keys(c.m.Imports)...)
	if c.r.core != nil && c.r.core != c.m {
		out = append(out, keys(c.r.core.Funcs)...)
	}
	return out
}
