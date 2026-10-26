// Package check implements kern's ownership, initialization and access checker
// (spec §5, rules 5.1–5.5) over the resolved program.
package check

import (
	"math"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/resolve"
)

// initState is the ownership state of an owned variable at a program point.
type initState int

const (
	stUninit  initState = iota // declared, not initialized (or out storage not yet set)
	stInit                     // owned, initialized
	stMoved                    // moved out; no owner
	stPartial                  // out storage with some fields initialized
)

// varState is the flow state of one owned variable.
type varState struct {
	st      initState
	fields  map[string]bool // initialized fields while stPartial (out storage)
	movedAt ast.Pos
}

// flow is the abstract state at a program point: owned variables by symbol.
type flow struct {
	vars map[*resolve.Symbol]*varState
	dead bool // unreachable (after return/break/continue)
}

func newFlow() *flow { return &flow{vars: map[*resolve.Symbol]*varState{}} }

func (f *flow) clone() *flow {
	n := &flow{vars: make(map[*resolve.Symbol]*varState, len(f.vars)), dead: f.dead}
	for k, v := range f.vars {
		vs := &varState{st: v.st, movedAt: v.movedAt}
		if v.fields != nil {
			vs.fields = map[string]bool{}
			for fk, fv := range v.fields {
				vs.fields[fk] = fv
			}
		}
		n.vars[k] = vs
	}
	return n
}

func (f *flow) get(s *resolve.Symbol) *varState {
	if v, ok := f.vars[s]; ok {
		return v
	}
	v := &varState{st: stInit}
	f.vars[s] = v
	return v
}

// pathElemKind classifies a place path element.
type pathElemKind int

const (
	peField pathElemKind = iota
	peIndex
	peSlice
)

// pathElem is one step of a place path.
type pathElem struct {
	kind           pathElemKind
	name           string
	isConst        bool
	idx            int64 // constant index
	lo, hi         int64 // constant slice bounds; hi == -1 means open
	loOpen, hiOpen bool
}

// place is a root symbol plus a path. perm is the strongest access the place
// permits (edit for owned locals and edit bindings, view for view bindings).
type place struct {
	root *resolve.Symbol
	path []pathElem
	perm ast.Conv // ConvEdit or ConvView
}

// accKind is an access class (spec §5.4).
type accKind int

const (
	accView accKind = iota
	accEdit
	accConsume
	accInit
	accFreeze // an enclosing binding's underlying place: no access permitted
)

func (k accKind) String() string {
	switch k {
	case accView:
		return "view"
	case accEdit:
		return "edit"
	case accConsume:
		return "consume"
	case accInit:
		return "init"
	}
	return "binding"
}

// access is one live access interval within a statement (or an enclosing
// binding, with an unbounded interval).
type access struct {
	pl      place
	kind    accKind
	start   int
	end     int
	pos     ast.Pos
	release bool // carries a pending release function
	label   string
}

const forever = math.MaxInt32

func overlaps(a, b *access) bool {
	if a.pl.root != b.pl.root {
		return false
	}
	if a.end < b.start || b.end < a.start {
		return false
	}
	return pathsOverlap(a.pl.path, b.pl.path)
}

// pathsOverlap implements the spec §5.4 overlap definition: prefix relation,
// with the constant-index and constant-slice disjointness exceptions.
func pathsOverlap(a, b []pathElem) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		switch {
		case x.kind == peField && y.kind == peField:
			if x.name != y.name {
				return false
			}
		case x.kind == peIndex && y.kind == peIndex:
			if x.isConst && y.isConst && x.idx != y.idx {
				return false
			}
			if !x.isConst || !y.isConst {
				return true
			}
		case x.kind == peSlice || y.kind == peSlice:
			if rangesDisjoint(x, y) {
				return false
			}
			return true
		default:
			return true
		}
	}
	return true
}

// rangesDisjoint reports whether two constant index/slice elements denote
// provably disjoint ranges.
func rangesDisjoint(x, y pathElem) bool {
	lo1, hi1, ok1 := bounds(x)
	lo2, hi2, ok2 := bounds(y)
	if !ok1 || !ok2 {
		return false
	}
	return hi1 <= lo2 || hi2 <= lo1
}

func bounds(e pathElem) (int64, int64, bool) {
	switch e.kind {
	case peIndex:
		if !e.isConst {
			return 0, 0, false
		}
		return e.idx, e.idx + 1, true
	case peSlice:
		if e.loOpen || e.hiOpen || !e.isConst {
			return 0, 0, false
		}
		return e.lo, e.hi, true
	}
	return 0, 0, false
}

func conflicts(a, b *access) bool {
	if !overlaps(a, b) {
		return false
	}
	if a.kind == accFreeze || b.kind == accFreeze {
		return true
	}
	return a.kind != accView || b.kind != accView
}
