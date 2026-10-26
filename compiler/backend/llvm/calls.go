package llvm

import (
	"fmt"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// typedArgs renders "ty val, ty val" for call arguments.
func (fe *fnEmit) typedArgs(args []*ir.Value) string {
	var parts []string
	for _, a := range args {
		parts = append(parts, fe.argParts(a)...)
	}
	return strings.Join(parts, ", ")
}

// argParts renders one argument: a sequence access becomes two scalars
// (pointer, length), matching paramDecls.
func (fe *fnEmit) argParts(a *ir.Value) []string {
	if fe.isAccess(a) {
		if seq, _ := accessInfo(a.Type); seq {
			v := fe.v(a)
			p := fe.next("t")
			n := fe.next("t")
			fe.emit("%s = extractvalue { ptr, i64 } %s, 0", p, v)
			fe.emit("%s = extractvalue { ptr, i64 } %s, 1", n, v)
			return []string{"ptr " + p, "i64 " + n}
		}
	}
	return []string{fe.argType(a) + " " + fe.v(a)}
}

// callResult emits the call with the right result handling.
func (fe *fnEmit) directCall(dsts []*ir.Value, retTypes []resolve.Type, callee string, args string) {
	switch {
	case len(retTypes) == 0 || (len(retTypes) == 1 && isUnit(retTypes[0])):
		fe.emit("call void %s(%s)", callee, args)
		if len(dsts) == 1 {
			fe.vals[dsts[0]] = "zeroinitializer"
		}
	case len(retTypes) == 1:
		fe.emit("%s = call %s %s(%s)", fe.v(dsts[0]), fe.e.lty(retTypes[0]), callee, args)
	default:
		tt := fe.e.lty(&resolve.Tuple{Elems: retTypes})
		r := fe.next("t")
		fe.emit("%s = call %s %s(%s)", r, tt, callee, args)
		for k, d := range dsts {
			fe.emit("%s = extractvalue %s %s, %d", fe.v(d), tt, r, k)
		}
	}
}

func (fe *fnEmit) call(c *ir.Call) {
	if c.Intrinsic != "" {
		fe.intrinsic(c)
		return
	}
	if c.Fn != nil {
		ft, ok := c.Fn.Type.(*resolve.Func)
		if !ok {
			fe.fail("indirect call through %s", c.Fn.Type)
			return
		}
		rets := ft.Ret
		var dsts []*ir.Value
		dsts = append(dsts, c.Dsts...)
		fe.directCall(dsts, rets, fe.v(c.Fn), fe.typedArgs(c.Args))
		return
	}
	callee := fe.e.mod.Func(c.Callee)
	if callee == nil {
		fe.fail("unknown callee %s", c.Callee)
		return
	}
	sym := fnSym(c.Callee)
	if callee.Extern != "" {
		sym = "@" + bareName(c.Callee)
	}
	fe.directCall(c.Dsts, callee.Rets, sym, fe.typedArgs(c.Args))
}

// release calls an accessor's release function with its receiver.
func (fe *fnEmit) release(i *ir.AccessEnd) {
	if strings.Contains(i.Release, ".") && fe.e.mod.Func(i.Release) == nil {
		// runtime intrinsic release (e.g. Mutex.unlock)
		fe.intrinsic(&ir.Call{Intrinsic: i.Release, Args: []*ir.Value{i.Recv}})
		return
	}
	fe.directCall(nil, nil, fnSym(i.Release), fe.typedArgs([]*ir.Value{i.Recv}))
}

// ---- runtime intrinsics ----

// isAccess reports whether v holds an address (or {ptr, len}) rather than
// a value: access values from access_begin, and values of access type.
func (fe *fnEmit) isAccess(v *ir.Value) bool {
	if fe.accs[v] {
		return true
	}
	_, ok := v.Type.(*resolve.Access)
	return ok
}

// argType is the register type of a call argument.
func (fe *fnEmit) argType(v *ir.Value) string {
	if fe.isAccess(v) {
		return fe.e.accessLty(v.Type)
	}
	return fe.e.lty(v.Type)
}

// isAccessType reports whether t is an access type (a spawned argument of
// access type is an access value).
func isAccessType(t resolve.Type) bool {
	_, ok := t.(*resolve.Access)
	return ok
}

// handle loads the opaque runtime handle behind an access to a library object.
func (fe *fnEmit) handle(v *ir.Value) string {
	if !fe.isAccess(v) {
		return fe.v(v) // an owned handle value
	}
	r := fe.next("t")
	fe.emit("%s = load ptr, ptr %s", r, fe.v(v))
	return r
}

// span renders a source span as the two i64 arguments runtime traps take.
func (fe *fnEmit) span(p ast.Pos) string {
	return fmt.Sprintf("i64 %d, i64 %d", p.Start, p.End)
}

// spill stores a value to a temporary and returns its address.
func (fe *fnEmit) spill(v *ir.Value) string {
	if a, ok := fe.addrs[v]; ok {
		return a // already a private copy in memory
	}
	a := fe.alloca(fe.e.mty(v.Type))
	fe.storeValue(v, a)
	return a
}

func (fe *fnEmit) elemType(c *ir.Call, i int) resolve.Type {
	if i < len(c.TypeArgs) && c.TypeArgs[i] != nil {
		return c.TypeArgs[i]
	}
	return &resolve.Unit{}
}

func (fe *fnEmit) unionOut(dst *ir.Value) (string, int) {
	n := dst.Type.(*resolve.Named)
	li := layout.Of(n)
	a := fe.alloca(fe.e.namedType(n))
	return a, li.Payload
}

func (fe *fnEmit) loadResult(dst *ir.Value, ptr string) {
	fe.loadFrom(fe.v(dst), dst.Type, ptr)
}

func (fe *fnEmit) rt(name, sig string, args string) string {
	fe.e.declare(name, sig)
	ret := sig[:strings.Index(sig, " (")]
	if ret == "void" {
		fe.emit("call void @%s(%s)", name, args)
		return ""
	}
	r := fe.next("t")
	fe.emit("%s = call %s @%s(%s)", r, ret, name, args)
	return r
}

func (fe *fnEmit) intrinsic(c *ir.Call) {
	e := fe.e
	a := c.Args
	dst := func() *ir.Value {
		if len(c.Dsts) > 0 {
			return c.Dsts[0]
		}
		return nil
	}
	setDst := func(r string) {
		if d := dst(); d != nil {
			fe.vals[d] = r
		}
	}
	sz := func(t resolve.Type) string { return fmt.Sprintf("%d", layout.Of(t).Size) }
	al := func(t resolve.Type) string { return fmt.Sprintf("%d", layout.Of(t).Align) }
	switch c.Intrinsic {
	case "print_u64", "print_i64":
		fe.rt(c.Intrinsic, "void (i64)", "i64 "+fe.v(a[0]))
	case "print_bool":
		z := fe.next("t")
		fe.emit("%s = zext i1 %s to i8", z, fe.v(a[0]))
		fe.rt("print_bool", "void (i8)", "i8 "+z)
	case "print_str":
		p := fe.next("t")
		fe.emit("%s = extractvalue { ptr, i64 } %s, 0", p, fe.v(a[0]))
		l := fe.next("t")
		fe.emit("%s = extractvalue { ptr, i64 } %s, 1", l, fe.v(a[0]))
		fe.rt("print_str", "void (ptr, i64)", "ptr "+p+", i64 "+l)
	case "println":
		fe.rt("println", "void ()", "")
	case "now_ns":
		setDst(fe.rt("kern_now_ns", "i64 ()", ""))
	case "fence":
		fe.rt("kern_fence", "void (i8)", "i8 "+fe.v(a[0]))
	case "clone":
		et := fe.elemType(c, 0)
		if n, ok := et.(*resolve.Named); ok && layout.Opaque(n) {
			h := fe.handle(a[0])
			switch n.Decl.Name {
			case "Vec":
				setDst(fe.rt("kern_vec_clone", "ptr (ptr)", "ptr "+h))
			case "Box":
				setDst(fe.rt("kern_box_clone", "ptr (ptr)", "ptr "+h))
			case "Shared":
				setDst(fe.rt("kern_shared_clone", "ptr (ptr)", "ptr "+h))
			default:
				fe.fail("clone of %s is not supported natively", n)
			}
			return
		}
		fe.loadFrom(fe.v(dst()), dst().Type, fe.v(a[0]))
	// Box
	case "Box.new":
		et := a[0].Type
		setDst(fe.rt("kern_box_new", "ptr (i64, ptr, i64, i64)", "i64 "+sz(et)+", ptr "+fe.spill(a[0])+", "+fe.span(c.Pos)))
	case "Box.get", "Box.at":
		setDst(fe.rt("kern_box_get", "ptr (ptr)", "ptr "+fe.handle(a[0])))
	case "Box.into":
		d := dst()
		out := fe.alloca(e.mty(d.Type))
		fe.rt("kern_box_into", "void (ptr, ptr, i64)", "ptr "+fe.v(a[0])+", ptr "+out+", i64 "+sz(d.Type))
		fe.loadResult(d, out)
	// Vec
	case "Vec.new":
		et := fe.elemType(c, 0)
		setDst(fe.rt("kern_vec_new", "ptr (i64, i64, i64)", "i64 "+sz(et)+", i64 "+al(et)+", i64 0"))
	case "Vec.with_capacity":
		et := fe.elemType(c, 0)
		setDst(fe.rt("kern_vec_new", "ptr (i64, i64, i64)", "i64 "+sz(et)+", i64 "+al(et)+", i64 "+fe.v(a[0])))
	case "Vec.len":
		setDst(fe.rt("kern_vec_len", "i64 (ptr)", "ptr "+fe.handle(a[0])))
	case "Vec.push":
		fe.rt("kern_vec_push", "void (ptr, ptr, i64, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.spill(a[1])+", "+fe.span(c.Pos))
	case "Vec.pop":
		d := dst()
		out, off := fe.unionOut(d)
		fe.rt("kern_vec_pop", "void (ptr, ptr, i64)", "ptr "+fe.handle(a[0])+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
		fe.loadResult(d, out)
	case "Vec.get", "Vec.at":
		setDst(fe.rt("kern_vec_at", "ptr (ptr, i64, i64, i64)", "ptr "+fe.handle(a[0])+", i64 "+fe.v(a[1])+", i64 "+fmt.Sprintf("%d", c.Pos.Start)+", i64 "+fmt.Sprintf("%d", c.Pos.End)))
	case "Vec.as_view", "Vec.as_edit":
		h := fe.handle(a[0])
		p := fe.rt("kern_vec_data", "ptr (ptr)", "ptr "+h)
		l := fe.rt("kern_vec_len", "i64 (ptr)", "ptr "+h)
		s1 := fe.next("t")
		fe.emit("%s = insertvalue { ptr, i64 } undef, ptr %s, 0", s1, p)
		fe.emit("%s = insertvalue { ptr, i64 } %s, i64 %s, 1", fe.v(dst()), s1, l)
	case "Vec.clear":
		et := fe.elemType(c, 0)
		fe.rt("kern_vec_clear", "void (ptr, ptr)", "ptr "+fe.handle(a[0])+", ptr "+e.glueFor(et))
	// Pool / Arena
	case "Pool.new":
		et := fe.elemType(c, 0)
		setDst(fe.rt("kern_pool_new", "ptr (i64, i64)", "i64 "+sz(et)+", i64 "+al(et)))
	case "Pool.alloc":
		d := dst()
		out, off := fe.unionOut(d)
		fe.rt("kern_pool_alloc", "void (ptr, ptr, ptr, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.spill(a[1])+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
		fe.loadResult(d, out)
	case "Pool.get", "Pool.at":
		setDst(fe.rt("kern_pool_at", "ptr (ptr, ptr, i64, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.spill(a[1])+", i64 "+fmt.Sprintf("%d", c.Pos.Start)+", i64 "+fmt.Sprintf("%d", c.Pos.End)))
	case "Pool.get2":
		h := fe.handle(a[0])
		x := fe.rt("kern_pool_get2", "ptr (ptr, ptr, ptr, i64, i64)", "ptr "+h+", ptr "+fe.spill(a[1])+", ptr "+fe.spill(a[2])+", i64 "+fmt.Sprintf("%d", c.Pos.Start)+", i64 "+fmt.Sprintf("%d", c.Pos.End))
		y := fe.rt("kern_pool_at", "ptr (ptr, ptr, i64, i64)", "ptr "+h+", ptr "+fe.spill(a[2])+", i64 "+fmt.Sprintf("%d", c.Pos.Start)+", i64 "+fmt.Sprintf("%d", c.Pos.End))
		fe.vals[c.Dsts[0]] = x
		fe.vals[c.Dsts[1]] = y
	case "Pool.free":
		et := fe.elemType(c, 0)
		fe.rt("kern_pool_free", "void (ptr, ptr, ptr, i64, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.spill(a[1])+", ptr "+e.glueFor(et)+", i64 "+fmt.Sprintf("%d", c.Pos.Start)+", i64 "+fmt.Sprintf("%d", c.Pos.End))
	case "Pool.len":
		setDst(fe.rt("kern_pool_len", "i64 (ptr)", "ptr "+fe.handle(a[0])))
	case "Arena.new":
		et := fe.elemType(c, 0)
		setDst(fe.rt("kern_arena_new", "ptr (i64, i64)", "i64 "+sz(et)+", i64 "+al(et)))
	case "Arena.alloc":
		d := dst()
		out, off := fe.unionOut(d)
		fe.rt("kern_arena_alloc", "void (ptr, ptr, ptr, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.spill(a[1])+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
		fe.loadResult(d, out)
	case "Arena.get", "Arena.at":
		setDst(fe.rt("kern_arena_at", "ptr (ptr, ptr, i64, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.spill(a[1])+", i64 "+fmt.Sprintf("%d", c.Pos.Start)+", i64 "+fmt.Sprintf("%d", c.Pos.End)))
	case "Arena.len":
		setDst(fe.rt("kern_arena_len", "i64 (ptr)", "ptr "+fe.handle(a[0])))
	// Shared / Mutex / Atomic
	case "Shared.new":
		setDst(fe.rt("kern_shared_new", "ptr (i64, ptr)", "i64 "+sz(a[0].Type)+", ptr "+fe.spill(a[0])))
	case "Shared.get":
		setDst(fe.rt("kern_shared_get", "ptr (ptr)", "ptr "+fe.handle(a[0])))
	case "Shared.clone":
		setDst(fe.rt("kern_shared_clone", "ptr (ptr)", "ptr "+fe.handle(a[0])))
	case "Mutex.new":
		setDst(fe.rt("kern_mutex_new", "ptr (i64, ptr)", "i64 "+sz(a[0].Type)+", ptr "+fe.spill(a[0])))
	case "Mutex.lock":
		setDst(fe.rt("kern_mutex_lock", "ptr (ptr, i64, i64)", "ptr "+fe.handle(a[0])+", i64 "+fmt.Sprintf("%d", c.Pos.Start)+", i64 "+fmt.Sprintf("%d", c.Pos.End)))
	case "Mutex.unlock":
		fe.rt("kern_mutex_unlock", "void (ptr)", "ptr "+fe.handle(a[0]))
	case "Atomic.new":
		setDst(fe.rt("kern_atomic_new", "ptr (i64, ptr)", "i64 "+sz(a[0].Type)+", ptr "+fe.spill(a[0])))
	case "Atomic.load":
		et := fe.elemType(c, 0)
		r := fe.rt("kern_atomic_load", "i64 (ptr, i64, i8)", "ptr "+fe.handle(a[0])+", i64 "+sz(et)+", i8 "+fe.v(a[1]))
		fe.narrow(dst(), r)
	case "Atomic.store":
		et := fe.elemType(c, 0)
		fe.rt("kern_atomic_store", "void (ptr, i64, i64, i8)", "ptr "+fe.handle(a[0])+", i64 "+sz(et)+", i64 "+fe.widen(a[1])+", i8 "+fe.v(a[2]))
	case "Atomic.swap":
		et := fe.elemType(c, 0)
		r := fe.rt("kern_atomic_swap", "i64 (ptr, i64, i64, i8)", "ptr "+fe.handle(a[0])+", i64 "+sz(et)+", i64 "+fe.widen(a[1])+", i8 "+fe.v(a[2]))
		fe.narrow(dst(), r)
	case "Atomic.fetch_add", "Atomic.fetch_sub", "Atomic.fetch_and", "Atomic.fetch_or", "Atomic.fetch_xor":
		et := fe.elemType(c, 0)
		op := map[string]int{"Atomic.fetch_add": 0, "Atomic.fetch_sub": 1, "Atomic.fetch_and": 2, "Atomic.fetch_or": 3, "Atomic.fetch_xor": 4}[c.Intrinsic]
		r := fe.rt("kern_atomic_rmw", "i64 (ptr, i64, i64, i64, i8)", "ptr "+fe.handle(a[0])+", i64 "+sz(et)+", i64 "+fmt.Sprintf("%d", op)+", i64 "+fe.widen(a[1])+", i8 "+fe.v(a[2]))
		fe.narrow(dst(), r)
	case "Atomic.compare_exchange", "Atomic.compare_exchange_weak":
		et := fe.elemType(c, 0)
		d := dst()
		out, off := fe.unionOut(d)
		fe.rt("kern_atomic_cmpxchg", "void (ptr, i64, i64, i64, i8, i8, ptr, i64)", "ptr "+fe.handle(a[0])+", i64 "+sz(et)+", i64 "+fe.widen(a[1])+", i64 "+fe.widen(a[2])+", i8 "+fe.v(a[3])+", i8 "+fe.v(a[4])+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
		fe.loadResult(d, out)
	// threads
	case "thread.scope":
		setDst(fe.rt("kern_scope_new", "ptr ()", ""))
	case "thread.spawn", "scope.spawn":
		fe.spawn(c)
	case "Thread.join":
		d := dst()
		out := fe.alloca(e.mty(d.Type))
		fe.rt("kern_thread_join", "void (ptr, ptr, i64)", "ptr "+fe.v(a[0])+", ptr "+out+", i64 "+sz(d.Type))
		fe.loadResult(d, out)
	case "Join.get":
		d := dst()
		out := fe.alloca(e.mty(d.Type))
		fe.rt("kern_join_get", "void (ptr, ptr, i64)", "ptr "+fe.v(a[0])+", ptr "+out+", i64 "+sz(d.Type))
		fe.loadResult(d, out)
	case "trap.set_handler":
		setDst(fe.rt("kern_set_handler", "ptr (ptr)", "ptr "+fe.v(a[0])))
	// allocator
	case "SystemAllocator.new":
		setDst(fe.rt("kern_sysalloc_new", "ptr ()", ""))
	case "SystemAllocator.alloc":
		d := dst()
		out, off := fe.unionOut(d)
		fe.rt("kern_sysalloc_alloc", "void (ptr, i64, i64, ptr, i64)", "ptr "+fe.handle(a[0])+", i64 "+fe.v(a[1])+", i64 "+fe.v(a[2])+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
		fe.loadResult(d, out)
	case "SystemAllocator.free":
		fe.rt("kern_sysalloc_free", "void (ptr, ptr, i64, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.v(a[1])+", i64 "+fe.v(a[2])+", i64 "+fe.v(a[3]))
	// test hooks
	case "testing.seed_generation":
		fe.rt("kern_test_pool_seed_generation", "void (ptr, i64, i64)", "ptr "+fe.handle(a[0])+", i64 "+fe.widen(a[1])+", i64 "+fe.widen(a[2]))
	case "testing.seed_shared_count":
		fe.rt("kern_test_shared_seed_count", "void (ptr, i64)", "ptr "+fe.handle(a[0])+", i64 "+fe.v(a[1]))
	case "testing.fail_next_alloc":
		fe.rt("kern_test_fail_next_alloc", "void ()", "")
	default:
		fe.fail("intrinsic %s is not supported natively", c.Intrinsic)
	}
}

// widen renders an integer value as i64.
func (fe *fnEmit) widen(v *ir.Value) string {
	bits, signed := intBits(v.Type)
	if resolve.IsBool(v.Type) {
		r := fe.next("t")
		fe.emit("%s = zext i1 %s to i64", r, fe.v(v))
		return r
	}
	if bits == 64 {
		return fe.v(v)
	}
	r := fe.next("t")
	if signed {
		fe.emit("%s = sext %s %s to i64", r, fe.e.lty(v.Type), fe.v(v))
	} else {
		fe.emit("%s = zext %s %s to i64", r, fe.e.lty(v.Type), fe.v(v))
	}
	return r
}

// narrow converts an i64 runtime result to dst's type.
func (fe *fnEmit) narrow(dst *ir.Value, r string) {
	if dst == nil {
		return
	}
	if resolve.IsBool(dst.Type) {
		fe.emit("%s = trunc i64 %s to i1", fe.v(dst), r)
		return
	}
	bits, _ := intBits(dst.Type)
	if bits == 64 {
		fe.vals[dst] = r
		return
	}
	fe.emit("%s = trunc i64 %s to %s", fe.v(dst), r, fe.e.lty(dst.Type))
}

// spawn packs the spawned function's arguments into a struct, emits a thunk
// that unpacks and calls it, and hands both to the runtime.
func (fe *fnEmit) spawn(c *ir.Call) {
	e := fe.e
	fnVal := c.Args[0]
	ft, ok := fnVal.Type.(*resolve.Func)
	if !ok {
		fe.fail("spawn of non-function")
		return
	}
	args := c.Args[1:]
	scope := ""
	if c.Intrinsic == "scope.spawn" {
		scope = fe.handle(args[0])
		args = args[1:]
	}
	// args struct: { fnptr, result, a0, a1, ... }
	var fields []string
	var retT string
	var retType resolve.Type = &resolve.Unit{}
	switch {
	case len(ft.Ret) > 1:
		retType = &resolve.Tuple{Elems: ft.Ret}
	case len(ft.Ret) == 1 && !isUnit(ft.Ret[0]):
		retType = ft.Ret[0]
	}
	if isUnit(retType) {
		retT = "{}"
	} else {
		retT = e.mty(retType)
	}
	fields = append(fields, "ptr", retT)
	for _, a := range args {
		fields = append(fields, e.mty(a.Type))
	}
	sty := "{ " + strings.Join(fields, ", ") + " }"
	pack := fe.alloca(sty)
	fe.emit("store ptr %s, ptr %s", fe.v(fnVal), pack)
	for k, a := range args {
		p := fe.next("t")
		fe.emit("%s = getelementptr %s, ptr %s, i32 0, i32 %d", p, sty, pack, k+2)
		fe.storeValue(a, p)
	}
	// the thunk
	e.thunks++
	thunk := fmt.Sprintf("@kern.thunk%d", e.thunks)
	var tb strings.Builder
	fmt.Fprintf(&tb, "define internal void %s(ptr %%pk) {\nentry:\n  %%fn = load ptr, ptr %%pk\n", thunk)
	var callArgs []string
	for k, a := range args {
		fmt.Fprintf(&tb, "  %%ap%d = getelementptr %s, ptr %%pk, i32 0, i32 %d\n", k, sty, k+2)
		seq, _ := accessInfo(a.Type)
		switch {
		case resolve.IsBool(a.Type):
			fmt.Fprintf(&tb, "  %%ab%d = load i8, ptr %%ap%d\n  %%a%d = trunc i8 %%ab%d to i1\n", k, k, k, k)
			callArgs = append(callArgs, "i1 %a"+fmt.Sprint(k))
		case seq && isAccessType(a.Type):
			fmt.Fprintf(&tb, "  %%a%d = load { ptr, i64 }, ptr %%ap%d\n  %%a%dp = extractvalue { ptr, i64 } %%a%d, 0\n  %%a%dn = extractvalue { ptr, i64 } %%a%d, 1\n", k, k, k, k, k, k)
			callArgs = append(callArgs, fmt.Sprintf("ptr %%a%dp, i64 %%a%dn", k, k))
		default:
			fmt.Fprintf(&tb, "  %%a%d = load %s, ptr %%ap%d\n", k, e.lty(a.Type), k)
			callArgs = append(callArgs, e.lty(a.Type)+" %a"+fmt.Sprint(k))
		}
	}
	if retT == "{}" {
		fmt.Fprintf(&tb, "  call void %%fn(%s)\n", strings.Join(callArgs, ", "))
	} else {
		fmt.Fprintf(&tb, "  %%r = call %s %%fn(%s)\n  %%rp = getelementptr %s, ptr %%pk, i32 0, i32 1\n", e.lty(retType), strings.Join(callArgs, ", "), sty)
		if resolve.IsBool(retType) {
			fmt.Fprintf(&tb, "  %%rz = zext i1 %%r to i8\n  store i8 %%rz, ptr %%rp\n")
		} else {
			fmt.Fprintf(&tb, "  store %s %%r, ptr %%rp\n", e.lty(retType))
		}
	}
	tb.WriteString("  ret void\n}\n")
	e.glueBody.WriteString(tb.String())
	d := c.Dsts[0]
	out, off := fe.unionOut(d)
	// the block's real layout: fn pointer, result, then the arguments
	var blockTypes []resolve.Type
	blockTypes = append(blockTypes, &resolve.RawPtr{Elem: &resolve.Unit{}})
	blockTypes = append(blockTypes, retType)
	for _, a := range args {
		blockTypes = append(blockTypes, a.Type)
	}
	bl := layout.Of(&resolve.Tuple{Elems: blockTypes})
	size := fmt.Sprintf("%d", bl.Size)
	roff := fmt.Sprintf("%d", bl.Fields[1])
	if c.Intrinsic == "scope.spawn" {
		fe.rt("kern_scope_spawn", "void (ptr, ptr, ptr, i64, i64, ptr, i64)", "ptr "+scope+", ptr "+thunk+", ptr "+pack+", i64 "+size+", i64 "+roff+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
	} else {
		fe.rt("kern_thread_spawn", "void (ptr, ptr, i64, i64, ptr, i64)", "ptr "+thunk+", ptr "+pack+", i64 "+size+", i64 "+roff+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
	}
	fe.loadResult(d, out)
}
