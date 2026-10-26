package ir

import (
	"fmt"

	"github.com/dshills/kern/compiler/resolve"
)

// Validate checks the five §7 invariants on every function of a module and
// returns one error per violation. It must pass after lowering and after
// every later pass.
func Validate(m *Module) []error {
	var errs []error
	for _, f := range m.Funcs {
		if f.Intrinsic || f.Extern != "" || f.Entry == nil {
			continue
		}
		v := &validator{f: f, consts: map[*Value]bool{}, defs: map[*Value]Instr{}}
		errs = append(errs, v.run()...)
	}
	return errs
}

type ownState int

const (
	owUninit ownState = iota
	owOwned
	owMoved
)

// vstate is the abstract state at a program point.
type vstate struct {
	own    map[*Local]ownState
	open   map[*Value]openAccess // open accesses by value
	unsafe int
}

type openAccess struct {
	edit   bool
	root   *Local
	path   []PathElem
	param  bool   // an access parameter, open for the whole function
	parent *Value // the access this one was reborrowed from, if any
}

func (s *vstate) clone() *vstate {
	n := &vstate{own: make(map[*Local]ownState, len(s.own)), open: make(map[*Value]openAccess, len(s.open)), unsafe: s.unsafe}
	for k, v := range s.own {
		n.own[k] = v
	}
	for k, v := range s.open {
		n.open[k] = v
	}
	return n
}

type validator struct {
	f      *Func
	errs   []error
	consts map[*Value]bool
	defs   map[*Value]Instr
	in     map[*Block]*vstate
}

func (v *validator) errorf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", v.f.Name, fmt.Sprintf(format, args...)))
}

func (v *validator) run() []error {
	// record constant values and definitions
	for _, b := range v.f.Blocks {
		for _, in := range b.Instrs {
			switch i := in.(type) {
			case *Const:
				v.consts[i.Dst] = true
				v.defs[i.Dst] = i
			case *AccessBegin:
				v.defs[i.Dst] = i
			case *Call:
				for _, d := range i.Dsts {
					v.defs[d] = i
				}
			}
		}
		if b.Term == nil {
			v.errorf("%s has no terminator", blockName(b))
		}
	}
	entry := &vstate{own: map[*Local]ownState{}, open: map[*Value]openAccess{}}
	for _, l := range v.f.Locals {
		entry.own[l] = owUninit
	}
	for _, p := range v.f.Params {
		if p.Conv == 3 || p.Conv == 0 { // own / by-value
			entry.own[p] = owOwned
		}
	}
	v.in = map[*Block]*vstate{v.f.Entry: entry}
	// worklist over the CFG; joins must agree (static moves), so at most a
	// few iterations are needed
	work := []*Block{v.f.Entry}
	visited := map[*Block]int{}
	for len(work) > 0 {
		b := work[0]
		work = work[1:]
		visited[b]++
		if visited[b] > 8 {
			v.errorf("%s: ownership state does not converge", blockName(b))
			continue
		}
		out := v.transfer(b, v.in[b].clone())
		for _, succ := range successors(b) {
			if cur, ok := v.in[succ]; !ok {
				v.in[succ] = out.clone()
				work = append(work, succ)
			} else if changed := v.join(succ, cur, out); changed {
				work = append(work, succ)
			}
		}
	}
	return v.errs
}

func successors(b *Block) []*Block {
	switch t := b.Term.(type) {
	case *Br:
		return []*Block{t.Target}
	case *CondBr:
		return []*Block{t.Then, t.Else}
	case *Switch:
		out := []*Block{}
		for _, c := range t.Cases {
			out = append(out, c)
		}
		if t.Default != nil {
			out = append(out, t.Default)
		}
		return out
	}
	return nil
}

// join merges an incoming state into a block's recorded state, reporting
// disagreement (invariant 1: static moves) and returning whether it changed.
func (v *validator) join(b *Block, cur, in *vstate) bool {
	changed := false
	for l, st := range in.own {
		cs, ok := cur.own[l]
		if !ok || cs == st {
			continue
		}
		// uninitialized and moved both carry no obligation; only owned
		// versus not-owned is a static-move disagreement (invariant 1)
		if st != owOwned && cs != owOwned {
			continue
		}
		if !l.Temp && !resolve.IsCopy(l.Type) {
			v.errorf("%s: local %s is %s on one path and %s on another", blockName(b), localName(l), stName(st), stName(cs))
		}
		cur.own[l] = owMoved
		changed = true
	}
	for val, oa := range in.open {
		if _, ok := cur.open[val]; !ok {
			v.errorf("%s: access %s is open on one path and closed on another", blockName(b), v.vname(val))
			cur.open[val] = oa
			changed = true
		}
	}
	for val := range cur.open {
		if _, ok := in.open[val]; !ok {
			v.errorf("%s: access %s is closed on one path and open on another", blockName(b), v.vname(val))
			delete(cur.open, val)
			changed = true
		}
	}
	if cur.unsafe != in.unsafe {
		v.errorf("%s: unsafe depth %d on one path and %d on another", blockName(b), cur.unsafe, in.unsafe)
		if in.unsafe > cur.unsafe {
			cur.unsafe = in.unsafe
			changed = true
		}
	}
	return changed
}

func stName(s ownState) string {
	switch s {
	case owOwned:
		return "owned"
	case owMoved:
		return "moved"
	}
	return "uninitialized"
}

func (v *validator) vname(x *Value) string { return v2(x) }

func v2(x *Value) string { return fmt.Sprintf("%%v%d", x.ID) }

// rootOf resolves a place to its root local and full path through access
// values.
func (v *validator) rootOf(p Place, st *vstate) (*Local, []PathElem, bool) {
	if p.Local != nil {
		return p.Local, p.Path, true
	}
	if p.Global != nil {
		return nil, nil, false // a thread_local: Copy, always initialized, no ownership
	}
	oa, ok := st.open[p.Access]
	if !ok {
		return nil, nil, false
	}
	if oa.root == nil {
		return nil, nil, false // a runtime-provided projection (library object)
	}
	path := append(append([]PathElem{}, oa.path...), p.Path...)
	return oa.root, path, true
}

func (v *validator) transfer(b *Block, st *vstate) *vstate {
	for _, in := range b.Instrs {
		switch i := in.(type) {
		case *Store:
			if _, isAcc := i.Src.Type.(*resolve.Access); isAcc {
				v.errorf("%s: second-class value %s stored into a place", blockName(b), v2(i.Src))
			}
			if i.Place.Local != nil && len(i.Place.Path) == 0 {
				if st.own[i.Place.Local] == owOwned && !resolve.IsCopy(i.Place.Local.Type) {
					v.errorf("%s: store over owned local %s without a drop", blockName(b), localName(i.Place.Local))
				}
				st.own[i.Place.Local] = owOwned
			}
		case *Init:
			if i.Place.Local != nil && len(i.Place.Path) == 0 {
				st.own[i.Place.Local] = owOwned
			}
		case *Move:
			if i.Src.Local != nil && len(i.Src.Path) == 0 {
				if st.own[i.Src.Local] != owOwned {
					v.errorf("%s: move of %s which is %s", blockName(b), localName(i.Src.Local), stName(st.own[i.Src.Local]))
				}
				st.own[i.Src.Local] = owMoved
			}
			v.checkNoConflict(b, st, i.Src, true, nil)
		case *Drop:
			if i.Place.Local != nil && len(i.Place.Path) == 0 {
				if st.own[i.Place.Local] != owOwned && !i.Place.Local.Temp {
					v.errorf("%s: drop of %s which is %s", blockName(b), localName(i.Place.Local), stName(st.own[i.Place.Local]))
				}
				st.own[i.Place.Local] = owMoved
			}
			v.checkNoConflict(b, st, i.Place, true, nil)
		case *AccessBegin:
			root, path, _ := v.rootOf(i.Place, st)
			isParam := i.Place.Local != nil && len(i.Place.Path) == 0 && (i.Place.Local.Conv == 1 || i.Place.Local.Conv == 2) && b == v.f.Entry
			oa := openAccess{edit: i.Edit, root: root, path: path, param: isParam}
			if i.Place.Access != nil {
				parent, ok := st.open[i.Place.Access]
				switch {
				case v.consts[i.Place.Access]:
					// a literal sequence (a string constant) is a read-only
					// access that is always open and never ends
					if i.Edit {
						v.errorf("%s: edit reborrow through a literal %s", blockName(b), v2(i.Place.Access))
					}
				case !ok:
					v.errorf("%s: reborrow through %s which is not open", blockName(b), v2(i.Place.Access))
				case i.Edit && !parent.edit:
					v.errorf("%s: edit reborrow through read-only access %s", blockName(b), v2(i.Place.Access))
				}
				oa.parent = i.Place.Access
			}
			if !isParam {
				v.checkNoConflict(b, st, i.Place, i.Edit, nil)
			}
			st.open[i.Dst] = oa
		case *AccessEnd:
			if _, ok := st.open[i.Access]; !ok {
				v.errorf("%s: access_end of %s which is not open", blockName(b), v2(i.Access))
			}
			delete(st.open, i.Access)
		case *Call:
			for _, d := range i.Dsts {
				if _, isAcc := d.Type.(*resolve.Access); isAcc {
					// a returned projection: rooted at the single access argument
					var root *Local
					var path []PathElem
					for _, a := range i.Args {
						if oa, ok := st.open[a]; ok {
							root, path = oa.root, oa.path
							break
						}
					}
					st.open[d] = openAccess{edit: d.Type.(*resolve.Access).Edit, root: root, path: path}
				}
			}
			if fn := v.calleeExtern(i); fn && st.unsafe == 0 {
				v.errorf("%s: extern call %s outside unsafe", blockName(b), i.Callee)
			}
		case *CheckOverflow:
			if i.Prov.Label == "" {
				v.errorf("%s: check_overflow without provenance", blockName(b))
			}
		case *CheckBounds:
			if i.Prov.Label == "" {
				v.errorf("%s: check_bounds without provenance", blockName(b))
			}
			if i.Prov.Label == "proven" && i.Prov.ProvenBy == "" {
				v.errorf("%s: proven check without proven_by", blockName(b))
			}
		case *CheckConv:
			if i.Prov.Label == "" {
				v.errorf("%s: check_conv without provenance", blockName(b))
			}
		case *UnsafeBegin:
			st.unsafe++
		case *UnsafeEnd:
			st.unsafe--
			if st.unsafe < 0 {
				v.errorf("%s: unsafe_end without unsafe_begin", blockName(b))
				st.unsafe = 0
			}
		case *MakeStruct:
			for _, f := range i.Fields {
				if _, isAcc := f.Type.(*resolve.Access); isAcc {
					v.errorf("%s: second-class value %s stored into a struct", blockName(b), v2(f))
				}
			}
		}
	}
	switch t := b.Term.(type) {
	case *Ret:
		for l, s := range st.own {
			if s == owOwned && !resolve.IsCopy(l.Type) && !l.Temp && l.Conv != 4 {
				v.errorf("%s: local %s is still owned at return", blockName(b), localName(l))
			}
		}
		returned := map[*Value]bool{}
		for i, val := range t.Vals {
			if _, isAcc := val.Type.(*resolve.Access); isAcc {
				if i >= len(v.f.Rets) {
					continue
				}
				if _, retAcc := v.f.Rets[i].(*resolve.Access); !retAcc {
					v.errorf("%s: second-class value %s returned from a value-returning function", blockName(b), v2(val))
				}
				if _, open := st.open[val]; !open {
					v.errorf("%s: returned access %s is not open", blockName(b), v2(val))
				}
				returned[val] = true
			}
		}
		for val, oa := range st.open {
			if oa.param || returned[val] {
				continue
			}
			v.errorf("%s: access %s is still open at return", blockName(b), v2(val))
		}
	}
	return st
}

// calleeExtern reports whether a call targets an extern function.
func (v *validator) calleeExtern(c *Call) bool {
	if c.Callee == "" {
		return false
	}
	// extern functions are named module.name and flagged on the Func; the
	// module is not available here, so the lowering marks extern calls by
	// an "extern " intrinsic prefix.
	return len(c.Intrinsic) > 7 && c.Intrinsic[:7] == "extern "
}

// checkNoConflict reports an open access that overlaps a new access or drop
// of place p (invariant 2), using the §5.4 overlap rule.
func (v *validator) checkNoConflict(b *Block, st *vstate, p Place, edit bool, skip *Value) {
	root, path, ok := v.rootOf(p, st)
	if !ok || root == nil {
		return
	}
	for val, oa := range st.open {
		if val == skip || oa.param || oa.root != root {
			continue
		}
		if !edit && !oa.edit {
			continue
		}
		if v.overlap(path, oa.path) {
			// a reborrow through an open access value is derived from that
			// value and its ancestors; only those are exempt
			if p.Access != nil && v.isAncestor(st, val, p.Access) {
				continue
			}
			v.errorf("%s: access to %s conflicts with open access %s", blockName(b), PlaceString(p), v2(val))
		}
	}
}

func (v *validator) overlap(a, b []PathElem) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		switch {
		case x.Kind == PathField && y.Kind == PathField:
			if x.Field != y.Field {
				return false
			}
		case x.Kind == PathVariant && y.Kind == PathVariant:
			if x.Tag != y.Tag || x.Field != y.Field {
				return false
			}
		case x.Kind == PathIndex && y.Kind == PathIndex:
			if v.consts[x.Index] && v.consts[y.Index] && v.constOf(x.Index) != v.constOf(y.Index) {
				return false
			}
			return true
		default:
			return true
		}
	}
	return true
}

func (v *validator) constOf(x *Value) uint64 {
	if c, ok := v.defs[x].(*Const); ok {
		return c.Int
	}
	return 0
}

// isAncestor reports whether anc is from or an ancestor of from in the
// reborrow chain.
func (v *validator) isAncestor(st *vstate, anc, from *Value) bool {
	for cur := from; cur != nil; {
		if cur == anc {
			return true
		}
		oa, ok := st.open[cur]
		if !ok {
			return false
		}
		cur = oa.parent
	}
	return false
}
