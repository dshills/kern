package llvm

import (
	"fmt"
	"math"
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
	args := fe.typedArgs(c.Args)
	if callee.Library {
		args = fe.withSpan(args, c.Pos)
	}
	fe.directCall(c.Dsts, callee.Rets, sym, args)
}

// release calls an accessor's release function with its receiver.
func (fe *fnEmit) release(i *ir.AccessEnd) {
	if strings.Contains(i.Release, ".") && fe.e.mod.Func(i.Release) == nil {
		// runtime intrinsic release (e.g. Mutex.unlock)
		fe.intrinsic(&ir.Call{Intrinsic: i.Release, Args: []*ir.Value{i.Recv}})
		return
	}
	args := fe.typedArgs([]*ir.Value{i.Recv})
	if rel := fe.e.mod.Func(i.Release); rel != nil && rel.Library {
		args = fe.withSpan(args, ast.Pos{})
	}
	fe.directCall(nil, nil, fnSym(i.Release), args)
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

// vecType is the LLVM vector type of a SIMD array type.
func (fe *fnEmit) vecType(t resolve.Type) (string, *resolve.Array) {
	arr := t.(*resolve.Array)
	return fmt.Sprintf("<%d x %s>", arr.Len, fe.e.lty(arr.Elem)), arr
}

// toVec loads an array value as a vector register.
func (fe *fnEmit) toVec(v *ir.Value) string {
	vt, arr := fe.vecType(v.Type)
	r := fe.next("t")
	// the storage is an array: only its element alignment is guaranteed
	fe.emit("%s = load %s, ptr %s, align %d", r, vt, fe.spill(v), layout.Of(arr.Elem).Align)
	return r
}

// fromVec stores a vector register as the array value dst.
func (fe *fnEmit) fromVec(dst *ir.Value, vec string) {
	vt, arr := fe.vecType(dst.Type)
	a := fe.alloca(fe.e.mty(dst.Type))
	fe.emit("store %s %s, ptr %s, align %d", vt, vec, a, layout.Of(arr.Elem).Align)
	if isLarge(dst.Type) {
		fe.addrs[dst] = a
		return
	}
	fe.emit("%s = load %s, ptr %s", fe.v(dst), fe.e.lty(dst.Type), a)
}

var simdOps = map[string][2]string{"simd_add": {"add", "fadd"}, "simd_sub": {"sub", "fsub"}, "simd_mul": {"mul", "fmul"}, "simd_and": {"and", ""}, "simd_or": {"or", ""}, "simd_xor": {"xor", ""}}

func (fe *fnEmit) simdBinary(c *ir.Call, a []*ir.Value) {
	vt, arr := fe.vecType(a[0].Type)
	op := simdOps[c.Intrinsic][0]
	if resolve.IsFloat(arr.Elem) {
		op = simdOps[c.Intrinsic][1]
	}
	l, r := fe.toVec(a[0]), fe.toVec(a[1])
	res := fe.next("t")
	fe.emit("%s = %s %s %s, %s", res, op, vt, l, r)
	fe.fromVec(c.Dsts[0], res)
}

// simdShuffle selects lanes from the concatenation of a and b by runtime
// indices (bounds-checked), lane by lane.
func (fe *fnEmit) simdShuffle(c *ir.Call, a []*ir.Value) {
	vt, arr := fe.vecType(a[0].Type)
	et := fe.e.lty(arr.Elem)
	l, r := fe.toVec(a[0]), fe.toVec(a[1])
	idx := fe.spill(a[2])
	cur := "undef"
	for i := 0; i < arr.Len; i++ {
		ip := fe.next("t")
		fe.emit("%s = getelementptr [%d x i32], ptr %s, i32 0, i32 %d", ip, arr.Len, idx, i)
		k := fe.next("t")
		fe.emit("%s = load i32, ptr %s", k, ip)
		big := fe.next("t")
		fe.emit("%s = icmp uge i32 %s, %d", big, k, 2*arr.Len)
		fe.check(big, "bounds", c.Pos)
		fromB := fe.next("t")
		fe.emit("%s = icmp uge i32 %s, %d", fromB, k, arr.Len)
		kb := fe.next("t")
		fe.emit("%s = sub i32 %s, %d", kb, k, arr.Len)
		ka := fe.next("t")
		fe.emit("%s = select i1 %s, i32 0, i32 %s", ka, fromB, k)
		kbb := fe.next("t")
		fe.emit("%s = select i1 %s, i32 %s, i32 0", kbb, fromB, kb)
		ea := fe.next("t")
		fe.emit("%s = extractelement %s %s, i32 %s", ea, vt, l, ka)
		eb := fe.next("t")
		fe.emit("%s = extractelement %s %s, i32 %s", eb, vt, r, kbb)
		sel := fe.next("t")
		fe.emit("%s = select i1 %s, %s %s, %s %s", sel, fromB, et, eb, et, ea)
		nxt := fe.next("t")
		fe.emit("%s = insertelement %s %s, %s %s, i32 %d", nxt, vt, cur, et, sel, i)
		cur = nxt
	}
	fe.fromVec(c.Dsts[0], cur)
}

func (fe *fnEmit) simdReduce(c *ir.Call, a []*ir.Value) {
	vt, arr := fe.vecType(a[0].Type)
	et := fe.e.lty(arr.Elem)
	v := fe.toVec(a[0])
	suffix := fmt.Sprintf("v%d%s", arr.Len, strings.ReplaceAll(strings.ReplaceAll(et, "double", "f64"), "float", "f32"))
	if resolve.IsFloat(arr.Elem) {
		name := "llvm.vector.reduce.fadd." + suffix
		fe.e.declare(name, et+" ("+et+", "+vt+")")
		fe.emit("%s = call %s @%s(%s %s, %s %s)", fe.v(c.Dsts[0]), et, name, et, fconst(arr.Elem, math.Copysign(0, -1)), vt, v)
		return
	}
	name := "llvm.vector.reduce.add." + suffix
	fe.e.declare(name, et+" ("+vt+")")
	fe.emit("%s = call %s @%s(%s %s)", fe.v(c.Dsts[0]), et, name, vt, v)
}

// asmEscape renders bytes as an LLVM string body.
func asmEscape(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f && c != '"' && c != '\\' {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "\\%02X", c)
		}
	}
	return sb.String()
}

// llvmOrderings maps the variants of Ordering (relaxed, acquire, release,
// acq_rel, seq_cst, in declaration order) to the LLVM ordering keywords.
var llvmOrderings = [...]string{"monotonic", "acquire", "release", "acq_rel", "seq_cst"}

// ordering renders a memory-ordering argument as its LLVM keyword. The
// checker requires every ordering to be a compile-time constant (spec §5.5),
// so the argument is a payload-free union literal whose tag the emitter
// recorded; the checker has also rejected the combinations LLVM forbids
// (a release or acq_rel failure ordering, a failure stronger than success).
func (fe *fnEmit) ordering(v *ir.Value) string {
	tag, ok := fe.tags[v]
	if !ok || tag < 0 || tag >= len(llvmOrderings) {
		fe.fail("memory ordering is not a constant")
	}
	return llvmOrderings[tag]
}

// cell is the address of an Atomic's cell: the access itself (the struct's
// only field is at offset 0), or a spill of an owned value.
func (fe *fnEmit) cell(v *ir.Value) string {
	if fe.isAccess(v) {
		if fe.packed[v] {
			// the resolver rejects an Atomic inside a packed struct
			// (TYPE_PACKED_ATOMIC); an access that reaches here anyway must
			// not promise the alignment the instruction requires
			fe.fail("atomic operation on a cell inside a packed struct")
		}
		return fe.v(v)
	}
	return fe.spill(v)
}

// memVal renders a value in its in-memory type (bool as i8), as an atomic
// instruction on the cell takes it.
func (fe *fnEmit) memVal(v *ir.Value) string {
	if resolve.IsBool(v.Type) {
		r := fe.next("t")
		fe.emit("%s = zext i1 %s to i8", r, fe.v(v))
		return r
	}
	return fe.v(v)
}

// fromMem gives dst the register form of an atomic instruction's result
// (bool back to i1).
func (fe *fnEmit) fromMem(dst *ir.Value, t resolve.Type, r string) {
	if dst == nil {
		return
	}
	if resolve.IsBool(t) {
		fe.emit("%s = trunc i8 %s to i1", fe.v(dst), r)
		return
	}
	fe.vals[dst] = r
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
// Inside a library function the span is the one its caller passed (the
// user-level call that entered the library), whatever p is.
func (fe *fnEmit) span(p ast.Pos) string {
	start, end := fe.spanParts(p)
	return "i64 " + start + ", i64 " + end
}

// spanParts renders the two span operands separately (phi entries).
func (fe *fnEmit) spanParts(p ast.Pos) (string, string) {
	if fe.f.Library {
		return "%span.s", "%span.e"
	}
	return fmt.Sprintf("%d", p.Start), fmt.Sprintf("%d", p.End)
}

// withSpan appends the span arguments a library callee takes.
func (fe *fnEmit) withSpan(args string, p ast.Pos) string {
	if args == "" {
		return fe.span(p)
	}
	return args + ", " + fe.span(p)
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
	case "yield_now":
		fe.rt("kern_yield", "void ()", "")
	case "exit":
		fe.rt("kern_exit", "void (i32)", "i32 "+fe.v(a[0]))
		fe.emit("unreachable")
		fe.emit("%s:", fe.next("dead")[1:])
	case "asm":
		tpl, ok := fe.strs[a[0]]
		if !ok {
			fe.fail("asm template must be a string literal")
			return
		}
		fe.emit("call void asm sideeffect \"%s\", \"~{memory}\"()", asmEscape(tpl))
	// SIMD: arrays reinterpreted as LLVM vectors through memory
	case "simd_add", "simd_sub", "simd_mul", "simd_and", "simd_or", "simd_xor":
		fe.simdBinary(c, a)
	case "simd_shuffle":
		fe.simdShuffle(c, a)
	case "simd_reduce_add":
		fe.simdReduce(c, a)
	// raw pointers (spec §6)
	case "ptr_null":
		fe.vals[dst()] = "null"
	case "ptr_from_addr":
		fe.emit("%s = inttoptr i64 %s to ptr", fe.v(dst()), fe.v(a[0]))
	case "ptr_addr":
		fe.emit("%s = ptrtoint ptr %s to i64", fe.v(dst()), fe.v(a[0]))
	case "ptr_cast":
		fe.vals[dst()] = fe.v(a[0])
	case "ptr_offset":
		et := fe.elemType(c, 0)
		m := fe.next("t")
		fe.emit("%s = mul i64 %s, %s", m, fe.v(a[1]), sz(et))
		fe.emit("%s = getelementptr i8, ptr %s, i64 %s", fe.v(dst()), fe.v(a[0]), m)
	case "ptr_load", "ptr_take":
		d := dst()
		if isLarge(d.Type) {
			fe.loadLarge(d, fe.v(a[0]))
			return
		}
		fe.loadFrom(fe.v(d), d.Type, fe.v(a[0]))
	case "ptr_store":
		fe.storeValue(a[1], fe.v(a[0]))
	case "ptr_load_volatile":
		d := dst()
		if isLarge(d.Type) {
			fe.loadLarge(d, fe.v(a[0])) // an aggregate device read is a byte copy
			return
		}
		if resolve.IsBool(d.Type) {
			r := fe.next("t")
			fe.emit("%s = load volatile i8, ptr %s", r, fe.v(a[0]))
			fe.emit("%s = trunc i8 %s to i1", fe.v(d), r)
			return
		}
		fe.emit("%s = load volatile %s, ptr %s", fe.v(d), e.lty(d.Type), fe.v(a[0]))
	case "ptr_store_volatile":
		t := a[1].Type
		if isLarge(t) {
			fe.storeValue(a[1], fe.v(a[0]))
			return
		}
		if resolve.IsBool(t) {
			r := fe.next("t")
			fe.emit("%s = zext i1 %s to i8", r, fe.v(a[1]))
			fe.emit("store volatile i8 %s, ptr %s", r, fe.v(a[0]))
			return
		}
		fe.emit("store volatile %s %s, ptr %s", e.lty(t), fe.v(a[1]), fe.v(a[0]))
	case "ptr_copy":
		et := fe.elemType(c, 0)
		m := fe.next("t")
		fe.emit("%s = mul i64 %s, %s", m, fe.v(a[2]), sz(et))
		e.declare("llvm.memmove.p0.p0.i64", "void (ptr, ptr, i64, i1)")
		fe.emit("call void @llvm.memmove.p0.p0.i64(ptr %s, ptr %s, i64 %s, i1 false)", fe.v(a[0]), fe.v(a[1]), m)
	case "ptr_store_seq":
		// the view's elements, len(src) * size_of[T] bytes, into the raw
		// destination; the ranges are disjoint by the caller's obligation
		et := fe.elemType(c, 0)
		p := fe.next("t")
		fe.emit("%s = extractvalue { ptr, i64 } %s, 0", p, fe.v(a[1]))
		n := fe.next("t")
		fe.emit("%s = extractvalue { ptr, i64 } %s, 1", n, fe.v(a[1]))
		m := fe.next("t")
		fe.emit("%s = mul i64 %s, %s", m, n, sz(et))
		e.declare("llvm.memcpy.p0.p0.i64", "void (ptr, ptr, i64, i1)")
		fe.emit("call void @llvm.memcpy.p0.p0.i64(ptr %s, ptr %s, i64 %s, i1 false)", fe.v(a[0]), p, m)
	case "ptr_view", "ptr_edit":
		// an access on raw memory is the pointer itself
		fe.vals[dst()] = fe.v(a[0])
	case "saturating_add", "saturating_sub":
		t := fe.elemType(c, 0)
		lt := e.mty(t)
		_, sg := intBits(t)
		pre, name := "u", "add"
		if sg {
			pre = "s"
		}
		if c.Intrinsic == "saturating_sub" {
			name = "sub"
		}
		intr := fmt.Sprintf("llvm.%s%s.sat.%s", pre, name, lt)
		e.declare(intr, fmt.Sprintf("%s (%s, %s)", lt, lt, lt))
		fe.emit("%s = call %s @%s(%s %s, %s %s)", fe.v(dst()), lt, intr, lt, fe.v(a[0]), lt, fe.v(a[1]))
	case "saturating_mul":
		t := fe.elemType(c, 0)
		lt := e.mty(t)
		bits, sg := intBits(t)
		res, ov := fe.withOverflow("mul", t, a[0], a[1])
		if !sg {
			fe.emit("%s = select i1 %s, %s -1, %s %s", fe.v(dst()), ov, lt, lt, res)
			break
		}
		// the limit in the direction of the overflow: the product's sign
		sl, sr, neg, lim := fe.next("t"), fe.next("t"), fe.next("t"), fe.next("t")
		fe.emit("%s = icmp slt %s %s, 0", sl, lt, fe.v(a[0]))
		fe.emit("%s = icmp slt %s %s, 0", sr, lt, fe.v(a[1]))
		fe.emit("%s = xor i1 %s, %s", neg, sl, sr)
		fe.emit("%s = select i1 %s, %s %d, %s %d", lim, neg, lt, minOf(bits), lt, maxOf(bits))
		fe.emit("%s = select i1 %s, %s %s, %s %s", fe.v(dst()), ov, lt, lim, lt, res)
	case "checked_add", "checked_sub", "checked_mul":
		t := fe.elemType(c, 0)
		res, ov := fe.withOverflow(strings.TrimPrefix(c.Intrinsic, "checked_"), t, a[0], a[1])
		fe.option(dst(), e.mty(t), ov, res)
	case "checked_div", "checked_rem":
		t := fe.elemType(c, 0)
		lt := e.mty(t)
		bits, sg := intBits(t)
		ov := fe.next("t")
		fe.emit("%s = icmp eq %s %s, 0", ov, lt, fe.v(a[1]))
		if sg {
			m1, m2, m, o2 := fe.next("t"), fe.next("t"), fe.next("t"), fe.next("t")
			fe.emit("%s = icmp eq %s %s, %d", m1, lt, fe.v(a[0]), minOf(bits))
			fe.emit("%s = icmp eq %s %s, -1", m2, lt, fe.v(a[1]))
			fe.emit("%s = and i1 %s, %s", m, m1, m2)
			fe.emit("%s = or i1 %s, %s", o2, ov, m)
			ov = o2
		}
		// the division runs on safe operands when it would trap, so the
		// untaken result is never undefined
		sl, sr, res := fe.next("t"), fe.next("t"), fe.next("t")
		fe.emit("%s = select i1 %s, %s 0, %s %s", sl, ov, lt, lt, fe.v(a[0]))
		fe.emit("%s = select i1 %s, %s 1, %s %s", sr, ov, lt, lt, fe.v(a[1]))
		op := map[bool]map[string]string{true: {"checked_div": "sdiv", "checked_rem": "srem"}, false: {"checked_div": "udiv", "checked_rem": "urem"}}[sg][c.Intrinsic]
		fe.emit("%s = %s %s %s, %s", res, op, lt, sl, sr)
		fe.option(dst(), lt, ov, res)
	case "checked_shl", "checked_shr":
		t := fe.elemType(c, 0)
		lt := e.mty(t)
		bits, sg := intBits(t)
		big := fe.next("t")
		fe.emit("%s = icmp uge i32 %s, %d", big, fe.v(a[1]), bits)
		cnt := fe.shiftCount(a[1], lt, bits, "wrapping") // masked: never poison
		res, ov := fe.next("t"), big
		if c.Intrinsic == "checked_shl" {
			back, lost, o2 := fe.next("t"), fe.next("t"), fe.next("t")
			fe.emit("%s = shl %s %s, %s", res, lt, fe.v(a[0]), cnt)
			// the representation's bits come back logically, as the trapping
			// form checks them (a set bit shifted out is an overflow)
			fe.emit("%s = lshr %s %s, %s", back, lt, res, cnt)
			fe.emit("%s = icmp ne %s %s, %s", lost, lt, back, fe.v(a[0]))
			fe.emit("%s = or i1 %s, %s", o2, big, lost)
			ov = o2
		} else if sg {
			fe.emit("%s = ashr %s %s, %s", res, lt, fe.v(a[0]), cnt)
		} else {
			fe.emit("%s = lshr %s %s, %s", res, lt, fe.v(a[0]), cnt)
		}
		fe.option(dst(), lt, ov, res)
	case "load_be", "load_le":
		// one load of the integer's width at byte offset a[1] of the view,
		// unaligned, byte-swapped for big-endian (the targets are little-endian)
		t := fe.elemType(c, 0)
		ty := e.mty(t)
		p := fe.next("t")
		fe.emit("%s = extractvalue { ptr, i64 } %s, 0", p, fe.v(a[0]))
		q := fe.next("t")
		fe.emit("%s = getelementptr i8, ptr %s, i64 %s", q, p, fe.v(a[1]))
		if c.Intrinsic == "load_le" || layout.Of(t).Size == 1 {
			fe.emit("%s = load %s, ptr %s, align 1", fe.v(dst()), ty, q)
		} else {
			r := fe.next("t")
			fe.emit("%s = load %s, ptr %s, align 1", r, ty, q)
			e.declare("llvm.bswap."+ty, ty+" ("+ty+")")
			fe.emit("%s = call %s @llvm.bswap.%s(%s %s)", fe.v(dst()), ty, ty, ty, r)
		}
	case "store_be", "store_le":
		// one store of the integer's width at byte offset a[1] of the edit,
		// unaligned, byte-swapped for big-endian
		t := fe.elemType(c, 0)
		ty := e.mty(t)
		p := fe.next("t")
		fe.emit("%s = extractvalue { ptr, i64 } %s, 0", p, fe.v(a[0]))
		q := fe.next("t")
		fe.emit("%s = getelementptr i8, ptr %s, i64 %s", q, p, fe.v(a[1]))
		val := fe.memVal(a[2])
		if c.Intrinsic == "store_be" && layout.Of(t).Size > 1 {
			r := fe.next("t")
			e.declare("llvm.bswap."+ty, ty+" ("+ty+")")
			fe.emit("%s = call %s @llvm.bswap.%s(%s %s)", r, ty, ty, ty, val)
			val = r
		}
		fe.emit("store %s %s, ptr %s, align 1", ty, val, q)
	case "ptr_view_seq", "ptr_edit_seq":
		s1 := fe.next("t")
		fe.emit("%s = insertvalue { ptr, i64 } undef, ptr %s, 0", s1, fe.v(a[0]))
		fe.emit("%s = insertvalue { ptr, i64 } %s, i64 %s, 1", fe.v(dst()), s1, fe.v(a[1]))
	case "ptr_edit2":
		fe.vals[c.Dsts[0]] = fe.v(a[0])
		fe.vals[c.Dsts[1]] = fe.v(a[1])
	case "size_of":
		fe.vals[dst()] = sz(fe.elemType(c, 0))
	case "align_of":
		fe.vals[dst()] = al(fe.elemType(c, 0))
	case "container_identity":
		setDst(fe.rt("kern_next_identity", "i32 ()", ""))
	case "fence":
		fe.emit("fence %s", fe.ordering(a[0]))
	case "clone":
		et := fe.elemType(c, 0)
		if n, ok := et.(*resolve.Named); ok && layout.Opaque(n) {
			h := fe.handle(a[0])
			switch n.Decl.Name {
			case "Shared":
				setDst(fe.rt("kern_shared_clone", "ptr (ptr)", "ptr "+h))
			default:
				fe.fail("clone of %s is not supported natively", n)
			}
			return
		}
		fe.loadFrom(fe.v(dst()), dst().Type, fe.v(a[0]))
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
		setDst(fe.rt("kern_mutex_lock", "ptr (ptr, i64, i64)", "ptr "+fe.handle(a[0])+", "+fe.span(c.Pos)))
	case "Mutex.unlock":
		fe.rt("kern_mutex_unlock", "void (ptr)", "ptr "+fe.handle(a[0]))
	// Condvar: wait is given the guard (the Mutex's data pointer, which the
	// checker requires to be the edit bound by a Mutex lock); the runtime
	// recovers the Mutex from it
	case "Condvar.new":
		setDst(fe.rt("kern_condvar_new", "ptr ()", ""))
	case "Condvar.wait":
		fe.rt("kern_condvar_wait", "void (ptr, ptr, i64, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.v(a[1])+", "+fe.span(c.Pos))
	case "Condvar.notify_one":
		fe.rt("kern_condvar_notify_one", "void (ptr)", "ptr "+fe.handle(a[0]))
	case "Condvar.notify_all":
		fe.rt("kern_condvar_notify_all", "void (ptr)", "ptr "+fe.handle(a[0]))
	// Atomic[T] is a struct of its cell (layout.Opaque excludes it): every
	// operation is an LLVM atomic instruction on the cell, in T's in-memory
	// type, with the ordering the checker required to be a constant
	case "Atomic.new":
		d := dst()
		et := fe.elemType(c, 0)
		fe.emit("%s = insertvalue %s zeroinitializer, %s %s, 0", fe.v(d), e.namedType(d.Type.(*resolve.Named)), e.mty(et), fe.memVal(a[0]))
	case "Atomic.load":
		et := fe.elemType(c, 0)
		r := fe.next("t")
		fe.emit("%s = load atomic %s, ptr %s %s, align %d", r, e.mty(et), fe.cell(a[0]), fe.ordering(a[1]), layout.Of(et).Size)
		fe.fromMem(dst(), et, r)
	case "Atomic.store":
		et := fe.elemType(c, 0)
		fe.emit("store atomic %s %s, ptr %s %s, align %d", e.mty(et), fe.memVal(a[1]), fe.cell(a[0]), fe.ordering(a[2]), layout.Of(et).Size)
	case "Atomic.swap", "Atomic.fetch_add", "Atomic.fetch_sub", "Atomic.fetch_and", "Atomic.fetch_or", "Atomic.fetch_xor":
		et := fe.elemType(c, 0)
		op := map[string]string{"Atomic.swap": "xchg", "Atomic.fetch_add": "add", "Atomic.fetch_sub": "sub", "Atomic.fetch_and": "and", "Atomic.fetch_or": "or", "Atomic.fetch_xor": "xor"}[c.Intrinsic]
		r := fe.next("t")
		fe.emit("%s = atomicrmw %s ptr %s, %s %s %s, align %d", r, op, fe.cell(a[0]), e.mty(et), fe.memVal(a[1]), fe.ordering(a[2]), layout.Of(et).Size)
		fe.fromMem(dst(), et, r)
	case "Atomic.compare_exchange", "Atomic.compare_exchange_weak":
		// Result[T, T]: Ok(old) on success, Err(current) on failure; both
		// variants carry the cell's prior value at the union's payload offset
		et := fe.elemType(c, 0)
		mt := e.mty(et)
		weak := ""
		if c.Intrinsic == "Atomic.compare_exchange_weak" {
			weak = "weak "
		}
		p := fe.next("t")
		fe.emit("%s = cmpxchg %sptr %s, %s %s, %s %s %s %s, align %d", p, weak, fe.cell(a[0]), mt, fe.memVal(a[1]), mt, fe.memVal(a[2]), fe.ordering(a[3]), fe.ordering(a[4]), layout.Of(et).Size)
		old := fe.next("t")
		ok := fe.next("t")
		fe.emit("%s = extractvalue { %s, i1 } %s, 0", old, mt, p)
		fe.emit("%s = extractvalue { %s, i1 } %s, 1", ok, mt, p)
		d := dst()
		out, off := fe.unionOut(d)
		tag := fe.next("t")
		fe.emit("store %s zeroinitializer, ptr %s", e.namedType(d.Type.(*resolve.Named)), out)
		fe.emit("%s = select i1 %s, i8 0, i8 1", tag, ok)
		fe.emit("store i8 %s, ptr %s", tag, out)
		pp := fe.next("t")
		fe.emit("%s = getelementptr i8, ptr %s, i64 %d", pp, out, off)
		fe.emit("store %s %s, ptr %s", mt, old, pp)
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
	case "SystemAllocator.alloc_zeroed":
		d := dst()
		out, off := fe.unionOut(d)
		fe.rt("kern_sysalloc_alloc_zeroed", "void (ptr, i64, i64, ptr, i64)", "ptr "+fe.handle(a[0])+", i64 "+fe.v(a[1])+", i64 "+fe.v(a[2])+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
		fe.loadResult(d, out)
	case "SystemAllocator.realloc":
		d := dst()
		out, off := fe.unionOut(d)
		fe.rt("kern_sysalloc_realloc", "void (ptr, ptr, i64, i64, i64, ptr, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.v(a[1])+", i64 "+fe.v(a[2])+", i64 "+fe.v(a[3])+", i64 "+fe.v(a[4])+", ptr "+out+", i64 "+fmt.Sprintf("%d", off))
		fe.loadResult(d, out)
	case "SystemAllocator.free":
		fe.rt("kern_sysalloc_free", "void (ptr, ptr, i64, i64)", "ptr "+fe.handle(a[0])+", ptr "+fe.v(a[1])+", i64 "+fe.v(a[2])+", i64 "+fe.v(a[3]))
	// test hooks
	case "testing.require_hooks":
		fe.rt("kern_test_require_hooks", "void ()", "")
	case "testing.seed_shared_count":
		fe.rt("kern_test_shared_seed_count", "void (ptr, i64)", "ptr "+fe.handle(a[0])+", i64 "+fe.v(a[1]))
	case "testing.fail_next_alloc":
		fe.rt("kern_test_fail_next_alloc", "void ()", "")
	default:
		fe.fail("intrinsic %s is not supported natively", c.Intrinsic)
	}
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

// withOverflow applies LLVM's overflow-reporting add, sub or mul to l and r
// in t's width and signedness, returning the result and overflow flag names.
func (fe *fnEmit) withOverflow(name string, t resolve.Type, l, r *ir.Value) (res, ov string) {
	lt := fe.e.mty(t)
	_, sg := intBits(t)
	pre := "u"
	if sg {
		pre = "s"
	}
	intr := fmt.Sprintf("llvm.%s%s.with.overflow.%s", pre, name, lt)
	fe.e.declare(intr, fmt.Sprintf("{ %s, i1 } (%s, %s)", lt, lt, lt))
	p := fe.next("t")
	fe.emit("%s = call { %s, i1 } @%s(%s %s, %s %s)", p, lt, intr, lt, fe.v(l), lt, fe.v(r))
	res, ov = fe.next("t"), fe.next("t")
	fe.emit("%s = extractvalue { %s, i1 } %s, 0", res, lt, p)
	fe.emit("%s = extractvalue { %s, i1 } %s, 1", ov, lt, p)
	return res, ov
}

// option stores an Option[T] result: None (tag 1) when ov, else Some (tag 0)
// of res at the payload offset, and loads it into d.
func (fe *fnEmit) option(d *ir.Value, lt, ov, res string) {
	out, off := fe.unionOut(d)
	fe.emit("store %s zeroinitializer, ptr %s", fe.e.namedType(d.Type.(*resolve.Named)), out)
	tag := fe.next("t")
	fe.emit("%s = select i1 %s, i8 1, i8 0", tag, ov)
	fe.emit("store i8 %s, ptr %s", tag, out)
	pp := fe.next("t")
	fe.emit("%s = getelementptr i8, ptr %s, i64 %d", pp, out, off)
	fe.emit("store %s %s, ptr %s", lt, res, pp)
	fe.loadResult(d, out)
}

// minOf and maxOf are the limits of a signed integer of bits width.
func minOf(bits int) int64 { return -(int64(1) << uint(bits-1)) }
func maxOf(bits int) int64 { return int64(1)<<uint(bits-1) - 1 }
