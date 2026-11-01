package interp

import (
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// destroyPlan is what destroy needs to know about one array or named type,
// computed once per type value instead of on every drop
// (specs/interp-destroy-plans): resolve.IsCopy re-derives markers through
// a visiting map keyed by the printed type, the destructor's name is a
// string concatenation, and substituting a field's type yields a fresh
// value each time, which nothing downstream could cache.
type destroyPlan struct {
	copy     bool             // a Copy type: nothing to destroy
	opaque   bool             // a library object (destroyLibrary)
	elem     resolve.Type     // an array's element type
	dtor     string           // the destructor's function name, "" if none
	fields   []resolve.Type   // a struct's field types, substituted
	variants [][]resolve.Type // a union's variants' field types, substituted
}

// maxDestroyPlans bounds the cache: past it, plans are built and used but
// not kept, so a program producing ever new type values cannot grow it.
const maxDestroyPlans = 65536

// planOf is t's plan, from this thread's cache when it has one.
func (it *Interp) planOf(t resolve.Type) *destroyPlan {
	if p, ok := it.plans[t]; ok {
		return p
	}
	p := buildPlan(t)
	if it.plans == nil {
		it.plans = map[resolve.Type]*destroyPlan{}
	}
	if len(it.plans) < maxDestroyPlans {
		it.plans[t] = p
	}
	return p
}

func buildPlan(t resolve.Type) *destroyPlan {
	if resolve.IsCopy(t) {
		return &destroyPlan{copy: true}
	}
	switch x := t.(type) {
	case *resolve.Array:
		return &destroyPlan{elem: x.Elem}
	case *resolve.Named:
		if layout.Opaque(x) {
			return &destroyPlan{opaque: true}
		}
		p := &destroyPlan{}
		args := typeArgs(x)
		if x.Decl.Destructor != nil {
			p.dtor = x.String() + ".destructor"
		}
		for _, f := range x.Decl.Fields {
			p.fields = append(p.fields, resolve.Subst(f.Type, args))
		}
		for _, vd := range x.Decl.Variants {
			var fs []resolve.Type
			for _, f := range vd.Fields {
				fs = append(fs, resolve.Subst(f.Type, args))
			}
			p.variants = append(p.variants, fs)
		}
		return p
	}
	return &destroyPlan{copy: true}
}
