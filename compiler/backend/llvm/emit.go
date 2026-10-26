package llvm

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// Emit renders a module as textual LLVM IR for the given target triple.
func Emit(mod *ir.Module, triple string) (string, []error) {
	e := &emitter{mod: mod, types: map[string]bool{}, glue: map[string]bool{}, runtime: map[string]bool{}}
	e.declare("kern_trap", "void (i32, i64, i64)")
	for _, g := range mod.Globals {
		fmt.Fprintf(&e.decls, "%s = thread_local global %s %s\n", globalSym(g), e.mty(g.Type), e.globalInit(g))
	}
	for _, f := range mod.Funcs {
		if f.Extern != "" && len(f.Blocks) == 0 {
			e.externDecl(f)
			continue
		}
		if f.Intrinsic {
			continue
		}
		e.function(f)
	}
	if mod.Entry != "" {
		entry := mod.Func(mod.Entry)
		ret := e.retType(entry)
		if ret == "void" {
			fmt.Fprintf(&e.sb, "define i32 @main() {\nentry:\n  call void @kern_start()\n  call %s %s()\n  ret i32 0\n}\n", retOrVoid(ret), fnSym(mod.Entry))
		} else {
			fmt.Fprintf(&e.sb, "define i32 @main() {\nentry:\n  call void @kern_start()\n  %%r = call %s %s()\n  %%s = %s %s %%r to i32\n  ret i32 %%s\n}\n", ret, fnSym(mod.Entry), widenOp(ret), ret)
		}
		e.declare("kern_start", "void ()")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "; kern %s\ntarget triple = \"%s\"\n\n", mod.Name, triple)
	for _, s := range e.strs {
		out.WriteString(s)
	}
	out.WriteString(e.decls.String())
	out.WriteString("\n")
	out.WriteString(e.glueBody.String())
	out.WriteString(e.sb.String())
	// every frame is probed page by page so a large frame cannot skip the
	// guard page (PLAN.md 2.2)
	out.WriteString("attributes #0 = { \"probe-stack\"=\"inline-asm\" }\n")
	return out.String(), e.errs
}

func retOrVoid(t string) string {
	if t == "{}" {
		return "{}"
	}
	return t
}

func widenOp(t string) string {
	switch t {
	case "i8", "i16":
		return "sext"
	case "i64":
		return "trunc"
	}
	return "bitcast"
}

func (e *emitter) externDecl(f *ir.Func) {
	var ps []string
	for _, p := range f.Params {
		t := e.lty(p.Type)
		if t == "i1" {
			t = "i1 zeroext" // C bool
		}
		ps = append(ps, t)
	}
	ret := "void"
	if len(f.Rets) == 1 && !isUnit(f.Rets[0]) {
		ret = e.lty(f.Rets[0])
		if ret == "i1" {
			ret = "zeroext i1"
		}
	}
	fmt.Fprintf(&e.decls, "declare %s @%s(%s)\n", ret, bareName(f.Name), strings.Join(ps, ", "))
}

// bareName strips the module prefix and specialization suffix from a
// mangled name: "m.puts" -> "puts".
func bareName(name string) string {
	if i := strings.Index(name, "["); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// ---- per-function state ----

type fnEmit struct {
	e       *emitter
	f       *ir.Func
	body    strings.Builder
	allocas strings.Builder
	tmp     int
	vals    map[*ir.Value]string // LLVM register names
	accs    map[*ir.Value]bool   // values defined by access_begin (they hold addresses)
	addrs   map[*ir.Value]string // large aggregate values kept in memory: their private copy
	labels  map[*ir.Block]string
	cur     *ir.Block
}

func (fe *fnEmit) next(prefix string) string {
	fe.tmp++
	return fmt.Sprintf("%%%s%d", prefix, fe.tmp)
}

func (fe *fnEmit) v(x *ir.Value) string {
	if a, ok := fe.addrs[x]; ok {
		// a register use of a value kept in memory: materialize it here
		r := fe.next("t")
		fe.emit("%s = load %s, ptr %s", r, fe.e.lty(x.Type), a)
		return r
	}
	if s, ok := fe.vals[x]; ok {
		return s
	}
	return fmt.Sprintf("%%v%d", x.ID)
}

// largeThreshold is the aggregate size above which values are kept in
// memory and moved with memcpy rather than as LLVM aggregate registers.
const largeThreshold = 128

// isLarge reports whether values of t are kept in memory.
func isLarge(t resolve.Type) bool {
	switch x := t.(type) {
	case *resolve.Array, *resolve.Tuple:
		return layout.Of(t).Size > largeThreshold
	case *resolve.Named:
		return !layout.Opaque(x) && layout.Of(t).Size > largeThreshold
	}
	return false
}

// copyTo copies size bytes between places.
func (fe *fnEmit) copyTo(dst, src string, size int) {
	fe.e.declare("llvm.memcpy.p0.p0.i64", "void (ptr, ptr, i64, i1)")
	fe.emit("call void @llvm.memcpy.p0.p0.i64(ptr %s, ptr %s, i64 %d, i1 false)", dst, src, size)
}

// zeroTo clears size bytes at a place.
func (fe *fnEmit) zeroTo(dst string, size int) {
	fe.e.declare("llvm.memset.p0.i64", "void (ptr, i8, i64, i1)")
	fe.emit("call void @llvm.memset.p0.i64(ptr %s, i8 0, i64 %d, i1 false)", dst, size)
}

// loadLarge gives dst a private copy of the aggregate at ptr.
func (fe *fnEmit) loadLarge(dst *ir.Value, ptr string) {
	a := fe.alloca(fe.e.mty(dst.Type))
	fe.copyTo(a, ptr, layout.Of(dst.Type).Size)
	fe.addrs[dst] = a
}

// storeValue stores src to ptr, by memcpy or memset for values kept in
// memory and by a register store otherwise.
func (fe *fnEmit) storeValue(src *ir.Value, ptr string) {
	if a, ok := fe.addrs[src]; ok {
		fe.copyTo(ptr, a, layout.Of(src.Type).Size)
		return
	}
	if isLarge(src.Type) && fe.vals[src] == "zeroinitializer" {
		fe.zeroTo(ptr, layout.Of(src.Type).Size)
		return
	}
	fe.storeTo(src.Type, fe.v(src), ptr)
}

func (fe *fnEmit) local(l *ir.Local) string { return fmt.Sprintf("%%l%d", l.ID) }

func (fe *fnEmit) emit(format string, args ...any) {
	fmt.Fprintf(&fe.body, "  "+format+"\n", args...)
}

// alloca reserves entry-block storage and returns its pointer register.
func (fe *fnEmit) alloca(lty string) string {
	r := fe.next("a")
	fmt.Fprintf(&fe.allocas, "  %s = alloca %s\n", r, lty)
	return r
}

func (fe *fnEmit) fail(format string, args ...any) {
	fe.e.errs = append(fe.e.errs, fmt.Errorf("%s: %s", fe.f.Name, fmt.Sprintf(format, args...)))
}

// paramType maps a parameter to its LLVM type.
func (e *emitter) paramType(p *ir.Local) string {
	switch p.Conv {
	case ast.ConvView, ast.ConvEdit:
		return e.accessLty(p.Type)
	case ast.ConvOut:
		return "ptr"
	}
	return e.lty(p.Type)
}

// accessLty is the register type of an access to t: a sequence access is
// {ptr, len}; every other access is a pointer. t may be the access type
// itself or the type accessed (parameters record the latter).
func (e *emitter) accessLty(t resolve.Type) string {
	if seq, _ := accessInfo(t); seq {
		return "{ ptr, i64 }"
	}
	return "ptr"
}

// accessInfo reports whether an access of (or to) t is a sequence access and
// the type it accesses.
func accessInfo(t resolve.Type) (seq bool, elem resolve.Type) {
	switch x := t.(type) {
	case *resolve.Access:
		if s, ok := x.Elem.(*resolve.Slice); ok {
			return true, s.Elem // view[[]T] is a sequence access too
		}
		return x.Seq, x.Elem
	case *resolve.Slice:
		return true, x.Elem
	}
	return false, t
}

// accessPlace builds the place an access register denotes.
func accessPlace(reg string, t resolve.Type) pl {
	seq, elem := accessInfo(t)
	if seq {
		return pl{ptr: reg, seq: true, elem: elem, ty: &resolve.Slice{Elem: elem}}
	}
	return pl{ptr: reg, ty: elem}
}

// globalSym is the symbol of a thread_local global.
func globalSym(g *ir.Global) string { return "@" + quote("g."+g.Name) }

// globalInit renders a thread_local's constant initializer.
func (e *emitter) globalInit(g *ir.Global) string {
	switch v := g.Init.(type) {
	case int64:
		return fmt.Sprintf("%d", v)
	case float64:
		return fconst(g.Type, v)
	case bool:
		if v {
			return "1"
		}
		return "0"
	}
	return "zeroinitializer"
}

// symOf is a function's definition symbol: the unmangled C name for an
// exported extern "C" function, the mangled name otherwise.
func (e *emitter) symOf(f *ir.Func) string {
	if f.Extern != "" {
		return "@" + bareName(f.Name)
	}
	return fnSym(f.Name)
}

// paramDecls renders one parameter's LLVM declaration(s): a sequence access
// is two scalars (pointer, length) so that aliasing attributes apply to the
// pointer. Aliasing per spec §5.4: `edit` is noalias, a `view` of a
// non-Interior type is noalias readonly, a `view` of an Interior type
// carries nothing; `out` is noalias.
func (e *emitter) paramDecls(f *ir.Func, p *ir.Local, i int) []string {
	switch p.Conv {
	case ast.ConvView, ast.ConvEdit:
		seq, elem := accessInfo(p.Type)
		attrs := ""
		if !resolve.IsInterior(elem) {
			attrs = " noalias"
			if p.Conv == ast.ConvView {
				attrs += " readonly"
			}
		}
		if seq {
			return []string{fmt.Sprintf("ptr%s %%p%d.p", attrs, i), fmt.Sprintf("i64 %%p%d.n", i)}
		}
		return []string{fmt.Sprintf("ptr%s %%p%d", attrs, i)}
	case ast.ConvOut:
		return []string{fmt.Sprintf("ptr noalias %%p%d", i)}
	}
	t := e.lty(p.Type)
	if t == "i1" && f.Extern != "" {
		t = "i1 zeroext" // C bool
	}
	return []string{fmt.Sprintf("%s %%p%d", t, i)}
}

func (e *emitter) retType(f *ir.Func) string {
	switch len(f.Rets) {
	case 0:
		return "void"
	case 1:
		if isUnit(f.Rets[0]) {
			return "void"
		}
		return e.lty(f.Rets[0])
	}
	return e.lty(&resolve.Tuple{Elems: f.Rets})
}

func (e *emitter) function(f *ir.Func) {
	fe := &fnEmit{e: e, f: f, vals: map[*ir.Value]string{}, accs: map[*ir.Value]bool{}, addrs: map[*ir.Value]string{}, labels: map[*ir.Block]string{}}
	for _, b := range f.Blocks {
		fe.labels[b] = fmt.Sprintf("bb%d", b.ID)
	}
	var ps []string
	for i, p := range f.Params {
		ps = append(ps, e.paramDecls(f, p, i)...)
	}
	// locals: allocas for every slot; parameters are stored into theirs
	for _, l := range f.Locals {
		switch l.Conv {
		case ast.ConvView, ast.ConvEdit, ast.ConvOut:
			if isParam(f, l) {
				fmt.Fprintf(&fe.allocas, "  %s = alloca %s\n", fe.local(l), e.paramType(l))
				continue
			}
		}
		fmt.Fprintf(&fe.allocas, "  %s = alloca %s\n", fe.local(l), e.mty(l.Type))
	}
	for i, p := range f.Params {
		pt := e.paramType(p)
		if p.Conv == ast.ConvOut || p.Conv == ast.ConvView || p.Conv == ast.ConvEdit {
			if pt == "{ ptr, i64 }" {
				// a sequence access arrives as two scalars
				fmt.Fprintf(&fe.allocas, "  %%p%d.a = insertvalue { ptr, i64 } undef, ptr %%p%d.p, 0\n  %%p%d = insertvalue { ptr, i64 } %%p%d.a, i64 %%p%d.n, 1\n", i, i, i, i, i)
			}
			fmt.Fprintf(&fe.allocas, "  store %s %%p%d, ptr %s\n", pt, i, fe.local(p))
			continue
		}
		if pt == "i1" {
			fmt.Fprintf(&fe.allocas, "  %%pz%d = zext i1 %%p%d to i8\n  store i8 %%pz%d, ptr %s\n", i, i, i, fe.local(p))
			continue
		}
		fmt.Fprintf(&fe.allocas, "  store %s %%p%d, ptr %s\n", pt, i, fe.local(p))
	}
	for _, b := range f.Blocks {
		fe.cur = b
		fmt.Fprintf(&fe.body, "%s:\n", fe.labels[b])
		for _, in := range b.Instrs {
			fe.instr(in)
		}
		fe.term(b.Term)
	}
	fmt.Fprintf(&e.sb, "define %s %s(%s) #0 {\nentry:\n%s  br label %%bb%d\n%s}\n\n", e.retType(f), e.symOf(f), strings.Join(ps, ", "), fe.allocas.String(), f.Entry.ID, fe.body.String())
}

func isParam(f *ir.Func, l *ir.Local) bool {
	for _, p := range f.Params {
		if p == l {
			return true
		}
	}
	return false
}

// ---- places ----

// pl is a computed place: a pointer with the pointee's static type, or a
// sequence {ptr, len} with its element type.
type pl struct {
	ptr    string // register holding ptr, or the {ptr,i64} aggregate when seq
	ty     resolve.Type
	seq    bool
	elem   resolve.Type
	direct bool // the place is an access-typed local itself: a load yields the access
}

func (fe *fnEmit) place(p ir.Place) pl {
	var cur pl
	if p.Local != nil {
		l := p.Local
		if (l.Conv == ast.ConvView || l.Conv == ast.ConvEdit || l.Conv == ast.ConvOut) && isParam(fe.f, l) {
			r := fe.next("t")
			pt := fe.e.paramType(l)
			fe.emit("%s = load %s, ptr %s", r, pt, fe.local(l))
			if l.Conv == ast.ConvOut {
				cur = pl{ptr: r, ty: l.Type}
			} else {
				cur = accessPlace(r, l.Type)
			}
		} else if _, isAcc := l.Type.(*resolve.Access); isAcc {
			// a local of access type holds the access itself
			r := fe.next("t")
			fe.emit("%s = load %s, ptr %s", r, fe.e.lty(l.Type), fe.local(l))
			cur = accessPlace(r, l.Type)
			cur.direct = len(p.Path) == 0
		} else {
			cur = pl{ptr: fe.local(l), ty: l.Type}
		}
	} else if p.Global != nil {
		cur = pl{ptr: globalSym(p.Global), ty: p.Global.Type}
	} else {
		cur = accessPlace(fe.v(p.Access), p.Access.Type)
	}
	for _, el := range p.Path {
		cur = fe.step(cur, el)
	}
	return cur
}

func (fe *fnEmit) step(cur pl, el ir.PathElem) pl {
	switch el.Kind {
	case ir.PathField:
		n, ok := cur.ty.(*resolve.Named)
		if !ok {
			fe.fail("field of %s", cur.ty)
			return cur
		}
		_, ft := resolve.FieldType(n, el.Name)
		r := fe.next("t")
		fe.emit("%s = getelementptr %s, ptr %s, i32 0, i32 %d", r, fe.e.namedType(n), cur.ptr, el.Field)
		return pl{ptr: r, ty: ft}
	case ir.PathVariant:
		n, ok := cur.ty.(*resolve.Named)
		if !ok {
			fe.fail("variant of %s", cur.ty)
			return cur
		}
		vd := n.Decl.Variants[el.Tag]
		offs := layout.VariantLayout(n, vd)
		fts := resolve.VariantFieldTypes(n, vd)
		r := fe.next("t")
		fe.emit("%s = getelementptr i8, ptr %s, i64 %d", r, cur.ptr, offs[el.Field])
		return pl{ptr: r, ty: fts[el.Field]}
	case ir.PathIndex:
		idx := fe.v(el.Index)
		if cur.seq {
			base := fe.next("t")
			fe.emit("%s = extractvalue { ptr, i64 } %s, 0", base, cur.ptr)
			r := fe.next("t")
			fe.emit("%s = getelementptr %s, ptr %s, i64 %s", r, fe.e.mty(cur.elem), base, idx)
			return pl{ptr: r, ty: cur.elem}
		}
		arr, ok := cur.ty.(*resolve.Array)
		if !ok {
			fe.fail("index of %s", cur.ty)
			return cur
		}
		r := fe.next("t")
		fe.emit("%s = getelementptr %s, ptr %s, i64 0, i64 %s", r, fe.e.lty(arr), cur.ptr, idx)
		return pl{ptr: r, ty: arr.Elem}
	case ir.PathSlice:
		lo, hi := fe.v(el.Lo), fe.v(el.Hi)
		var base string
		var elem resolve.Type
		if cur.seq {
			b := fe.next("t")
			fe.emit("%s = extractvalue { ptr, i64 } %s, 0", b, cur.ptr)
			base, elem = b, cur.elem
		} else {
			arr, ok := cur.ty.(*resolve.Array)
			if !ok {
				fe.fail("slice of %s", cur.ty)
				return cur
			}
			base, elem = cur.ptr, arr.Elem
		}
		p := fe.next("t")
		fe.emit("%s = getelementptr %s, ptr %s, i64 %s", p, fe.e.mty(elem), base, lo)
		ln := fe.next("t")
		fe.emit("%s = sub i64 %s, %s", ln, hi, lo)
		s1 := fe.next("t")
		fe.emit("%s = insertvalue { ptr, i64 } undef, ptr %s, 0", s1, p)
		s2 := fe.next("t")
		fe.emit("%s = insertvalue { ptr, i64 } %s, i64 %s, 1", s2, s1, ln)
		return pl{ptr: s2, seq: true, elem: elem, ty: &resolve.Slice{Elem: elem}}
	}
	return cur
}

// loadFrom loads a value of type t from a pointer (bool: i8 -> i1).
func (fe *fnEmit) loadFrom(dst string, t resolve.Type, ptr string) {
	if resolve.IsBool(t) {
		r := fe.next("t")
		fe.emit("%s = load i8, ptr %s", r, ptr)
		fe.emit("%s = trunc i8 %s to i1", dst, r)
		return
	}
	fe.emit("%s = load %s, ptr %s", dst, fe.e.lty(t), ptr)
}

// storeTo stores a value of type t to a pointer (bool: i1 -> i8).
func (fe *fnEmit) storeTo(t resolve.Type, val, ptr string) {
	if resolve.IsBool(t) {
		r := fe.next("t")
		fe.emit("%s = zext i1 %s to i8", r, val)
		fe.emit("store i8 %s, ptr %s", r, ptr)
		return
	}
	if isUnit(t) {
		return
	}
	fe.emit("store %s %s, ptr %s", fe.e.lty(t), val, ptr)
}

// ---- instructions ----

// check emits `br i1 cond, trap, cont` with a trap block for kind.
func (fe *fnEmit) check(cond string, kind string, pos ast.Pos) {
	fe.tmp++
	t := fmt.Sprintf("tr%d", fe.tmp)
	c := fmt.Sprintf("ok%d", fe.tmp)
	fe.emit("br i1 %s, label %%%s, label %%%s", cond, t, c)
	fmt.Fprintf(&fe.body, "%s:\n", t)
	fe.emit("call void @kern_trap(i32 %d, i64 %d, i64 %d)", trapKind(kind), pos.Start, pos.End)
	fe.emit("unreachable")
	fmt.Fprintf(&fe.body, "%s:\n", c)
}

var trapKinds = []string{"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "stack", "explicit", "unreachable"}

func trapKind(k string) int {
	for i, n := range trapKinds {
		if n == k {
			return i
		}
	}
	return len(trapKinds) - 1
}

func intBits(t resolve.Type) (int, bool) {
	p, ok := t.(*resolve.Prim)
	if !ok {
		return 0, false
	}
	switch p.Name {
	case "i8", "u8":
		return 8, p.Name[0] == 'i'
	case "i16", "u16":
		return 16, p.Name[0] == 'i'
	case "i32", "u32":
		return 32, p.Name[0] == 'i'
	case "i64", "u64", "isize", "usize":
		return 64, p.Name[0] == 'i'
	}
	return 0, false
}

func (fe *fnEmit) instr(in ir.Instr) {
	e := fe.e
	switch i := in.(type) {
	case *ir.Const:
		fe.constant(i)
	case *ir.Load:
		p := fe.place(i.Place)
		if p.seq || p.direct {
			fe.vals[i.Dst] = p.ptr
			return
		}
		if isLarge(i.Dst.Type) {
			fe.loadLarge(i.Dst, p.ptr)
			return
		}
		fe.loadFrom(fe.v(i.Dst), i.Dst.Type, p.ptr)
	case *ir.Store:
		p := fe.place(i.Place)
		fe.storeValue(i.Src, p.ptr)
	case *ir.Move:
		p := fe.place(i.Src)
		if isLarge(i.Dst.Type) {
			fe.loadLarge(i.Dst, p.ptr)
			return
		}
		fe.loadFrom(fe.v(i.Dst), i.Dst.Type, p.ptr)
	case *ir.Drop:
		if g := e.glueFor(i.Type); g != "null" {
			p := fe.place(i.Place)
			fe.emit("call void %s(ptr %s)", g, p.ptr)
		}
	case *ir.AccessBegin:
		fe.accs[i.Dst] = true
		p := fe.place(i.Place)
		if seq, _ := accessInfo(i.Dst.Type); seq && !p.seq {
			// an array place coerced to a sequence access
			arr, ok := p.ty.(*resolve.Array)
			if !ok {
				fe.fail("sequence access of %s", p.ty)
				return
			}
			s1 := fe.next("t")
			fe.emit("%s = insertvalue { ptr, i64 } undef, ptr %s, 0", s1, p.ptr)
			fe.emit("%s = insertvalue { ptr, i64 } %s, i64 %d, 1", fe.v(i.Dst), s1, arr.Len)
			return
		}
		fe.vals[i.Dst] = p.ptr
	case *ir.AccessEnd:
		if i.Release != "" {
			fe.release(i)
		}
	case *ir.Call:
		fe.call(i)
	case *ir.Binary:
		fe.binary(i)
	case *ir.Unary:
		t := e.lty(i.X.Type)
		if i.Op == "!" {
			fe.emit("%s = xor i1 %s, true", fe.v(i.Dst), fe.v(i.X))
		} else if resolve.IsFloat(i.X.Type) {
			fe.emit("%s = fneg %s %s", fe.v(i.Dst), t, fe.v(i.X))
		} else {
			fe.emit("%s = sub %s 0, %s", fe.v(i.Dst), t, fe.v(i.X))
		}
	case *ir.Conv:
		fe.conv(i)
	case *ir.CheckOverflow:
		fe.checkOverflow(i)
	case *ir.CheckBounds:
		if i.Index != nil {
			c := fe.next("t")
			fe.emit("%s = icmp uge i64 %s, %s", c, fe.v(i.Index), fe.v(i.Len))
			fe.check(c, "bounds", i.Pos)
			return
		}
		c1 := fe.next("t")
		fe.emit("%s = icmp ugt i64 %s, %s", c1, fe.v(i.Lo), fe.v(i.Hi))
		c2 := fe.next("t")
		fe.emit("%s = icmp ugt i64 %s, %s", c2, fe.v(i.Hi), fe.v(i.Len))
		c := fe.next("t")
		fe.emit("%s = or i1 %s, %s", c, c1, c2)
		fe.check(c, "bounds", i.Pos)
	case *ir.CheckConv:
		fe.checkConv(i)
	case *ir.Len:
		p := fe.place(i.Place)
		if p.seq {
			fe.emit("%s = extractvalue { ptr, i64 } %s, 1", fe.v(i.Dst), p.ptr)
			return
		}
		if arr, ok := p.ty.(*resolve.Array); ok {
			fe.vals[i.Dst] = fmt.Sprintf("%d", arr.Len)
			return
		}
		fe.fail("len of %s", p.ty)
	case *ir.MakeStruct:
		n := i.Dst.Type.(*resolve.Named)
		ty := e.namedType(n)
		cur := "zeroinitializer"
		args := typeArgs(n)
		for k, f := range i.Fields {
			ft := resolve.Subst(n.Decl.Fields[k].Type, args)
			val := fe.v(f)
			if resolve.IsBool(ft) {
				z := fe.next("t")
				fe.emit("%s = zext i1 %s to i8", z, val)
				val = z
			}
			r := fe.next("t")
			fe.emit("%s = insertvalue %s %s, %s %s, %d", r, ty, cur, e.mty(ft), val, k)
			cur = r
		}
		fe.vals[i.Dst] = cur
	case *ir.MakeUnion:
		fe.makeUnion(i)
	case *ir.MakeArray:
		arr := i.Dst.Type.(*resolve.Array)
		ty := e.lty(arr)
		cur := "zeroinitializer"
		for k, el := range i.Elems {
			val := fe.v(el)
			if resolve.IsBool(arr.Elem) {
				z := fe.next("t")
				fe.emit("%s = zext i1 %s to i8", z, val)
				val = z
			}
			r := fe.next("t")
			fe.emit("%s = insertvalue %s %s, %s %s, %d", r, ty, cur, e.mty(arr.Elem), val, k)
			cur = r
		}
		fe.vals[i.Dst] = cur
	case *ir.Zero:
		if resolve.IsBool(i.Dst.Type) {
			fe.vals[i.Dst] = "false"
		} else if resolve.IsFloat(i.Dst.Type) {
			fe.vals[i.Dst] = "0.0"
		} else if b, ok := intBits(i.Dst.Type); ok && b > 0 {
			fe.vals[i.Dst] = "0"
		} else {
			fe.vals[i.Dst] = "zeroinitializer"
		}
	case *ir.Tag:
		p := fe.place(i.Place)
		n := p.ty.(*resolve.Named)
		li := layout.Of(n)
		tagTy := "i8"
		if li.TagSize == 2 {
			tagTy = "i16"
		}
		r := fe.next("t")
		fe.emit("%s = load %s, ptr %s", r, tagTy, p.ptr)
		fe.emit("%s = zext %s %s to i32", fe.v(i.Dst), tagTy, r)
	case *ir.FuncRef:
		if f := e.mod.Func(i.Name); f != nil && f.Extern != "" {
			fe.vals[i.Dst] = "@" + bareName(i.Name)
		} else {
			fe.vals[i.Dst] = fnSym(i.Name)
		}
	case *ir.Trap:
		fe.emit("call void @kern_trap(i32 %d, i64 %d, i64 %d)", trapKind(i.Kind), i.Pos.Start, i.Pos.End)
	case *ir.Copy:
		if a, ok := fe.addrs[i.Src]; ok {
			fe.loadLarge(i.Dst, a)
			return
		}
		fe.vals[i.Dst] = fe.v(i.Src)
	case *ir.UnsafeBegin, *ir.UnsafeEnd, *ir.Init:
	default:
		fe.fail("unsupported instruction %T", in)
	}
}

func (fe *fnEmit) constant(c *ir.Const) {
	switch {
	case c.Unit:
		fe.vals[c.Dst] = "zeroinitializer"
	case c.Bytes != nil:
		e := fe.e
		name := fmt.Sprintf("@.str%d", len(e.strs))
		var sb strings.Builder
		for _, b := range c.Bytes {
			if b >= 0x20 && b < 0x7f && b != '"' && b != '\\' {
				sb.WriteByte(b)
			} else {
				fmt.Fprintf(&sb, "\\%02X", b)
			}
		}
		e.strs = append(e.strs, fmt.Sprintf("%s = private unnamed_addr constant [%d x i8] c\"%s\"\n", name, len(c.Bytes), sb.String()))
		s1 := fe.next("t")
		fe.emit("%s = insertvalue { ptr, i64 } undef, ptr %s, 0", s1, name)
		fe.emit("%s = insertvalue { ptr, i64 } %s, i64 %d, 1", fe.v(c.Dst), s1, len(c.Bytes))
	case resolve.IsBool(c.Dst.Type):
		if c.Bool {
			fe.vals[c.Dst] = "true"
		} else {
			fe.vals[c.Dst] = "false"
		}
	case resolve.IsFloat(c.Dst.Type):
		fe.vals[c.Dst] = fconst(c.Dst.Type, c.Float)
	default:
		v := c.Int
		if c.Neg {
			v = -v
		}
		bits, _ := intBits(c.Dst.Type)
		if bits > 0 && bits < 64 {
			v &= 1<<uint(bits) - 1
		}
		if c.Neg {
			fe.vals[c.Dst] = fmt.Sprintf("%d", int64(v))
		} else {
			fe.vals[c.Dst] = fmt.Sprintf("%d", v)
		}
	}
}

func (fe *fnEmit) makeUnion(i *ir.MakeUnion) {
	n := i.Dst.Type.(*resolve.Named)
	ty := fe.e.namedType(n)
	li := layout.Of(n)
	tmp := fe.alloca(ty)
	fe.emit("store %s zeroinitializer, ptr %s", ty, tmp)
	tagTy := "i8"
	if li.TagSize == 2 {
		tagTy = "i16"
	}
	fe.emit("store %s %d, ptr %s", tagTy, i.Tag, tmp)
	vd := n.Decl.Variants[i.Tag]
	offs := layout.VariantLayout(n, vd)
	for k, f := range i.Fields {
		p := fe.next("t")
		fe.emit("%s = getelementptr i8, ptr %s, i64 %d", p, tmp, offs[k])
		fe.storeValue(f, p)
	}
	if isLarge(i.Dst.Type) {
		fe.addrs[i.Dst] = tmp
		return
	}
	fe.emit("%s = load %s, ptr %s", fe.v(i.Dst), ty, tmp)
}

var intOps = map[string]string{"+": "add", "-": "sub", "*": "mul", "&": "and", "|": "or", "^": "xor", "<<": "shl"}
var cmpOps = map[string]string{"==": "eq", "!=": "ne", "<": "lt", "<=": "le", ">": "gt", ">=": "ge"}
var fops = map[string]string{"+": "fadd", "-": "fsub", "*": "fmul", "/": "fdiv", "%": "frem"}
var fcmps = map[string]string{"==": "oeq", "!=": "une", "<": "olt", "<=": "ole", ">": "ogt", ">=": "oge"}

func (fe *fnEmit) binary(i *ir.Binary) {
	t := i.L.Type
	lt := fe.e.lty(t)
	l, r := fe.v(i.L), fe.v(i.R)
	dst := fe.v(i.Dst)
	if resolve.IsBool(t) {
		switch i.Op {
		case "&&":
			fe.emit("%s = and i1 %s, %s", dst, l, r)
		case "||":
			fe.emit("%s = or i1 %s, %s", dst, l, r)
		case "==":
			fe.emit("%s = icmp eq i1 %s, %s", dst, l, r)
		case "!=":
			fe.emit("%s = icmp ne i1 %s, %s", dst, l, r)
		}
		return
	}
	if resolve.IsFloat(t) {
		if op, ok := fops[i.Op]; ok {
			fe.emit("%s = %s %s %s, %s", dst, op, lt, l, r)
			return
		}
		fe.emit("%s = fcmp %s %s %s, %s", dst, fcmps[i.Op], lt, l, r)
		return
	}
	bits, signed := intBits(t)
	if bits == 0 {
		// aggregate equality: structural, through the type's equality glue
		if i.Op == "==" || i.Op == "!=" {
			a := fe.spill(i.L)
			b := fe.spill(i.R)
			c := fe.next("t")
			fe.emit("%s = call i1 %s(ptr %s, ptr %s)", c, fe.e.eqFor(t), a, b)
			if i.Op == "==" {
				fe.vals[i.Dst] = c
			} else {
				fe.emit("%s = xor i1 %s, true", dst, c)
			}
			return
		}
		fe.fail("binary %s on %s", i.Op, t)
		return
	}
	if op, ok := cmpOps[i.Op]; ok {
		if op != "eq" && op != "ne" {
			if signed {
				op = "s" + op
			} else {
				op = "u" + op
			}
		}
		fe.emit("%s = icmp %s %s %s, %s", dst, op, lt, l, r)
		return
	}
	switch i.Op {
	case "/":
		if signed {
			fe.emit("%s = sdiv %s %s, %s", dst, lt, l, r)
		} else {
			fe.emit("%s = udiv %s %s, %s", dst, lt, l, r)
		}
	case "%":
		if signed {
			fe.emit("%s = srem %s %s, %s", dst, lt, l, r)
		} else {
			fe.emit("%s = urem %s %s, %s", dst, lt, l, r)
		}
	case ">>":
		c := fe.shiftCount(i.R, lt, bits, i.Mode)
		if signed {
			fe.emit("%s = ashr %s %s, %s", dst, lt, l, c)
		} else {
			fe.emit("%s = lshr %s %s, %s", dst, lt, l, c)
		}
	case "<<":
		c := fe.shiftCount(i.R, lt, bits, i.Mode)
		fe.emit("%s = shl %s %s, %s", dst, lt, l, c)
	default:
		fe.emit("%s = %s %s %s, %s", dst, intOps[i.Op], lt, l, r)
	}
}

// shiftCount converts a u32 shift count to the operand width (wrapping
// shifts mask it to width-1).
func (fe *fnEmit) shiftCount(r *ir.Value, lt string, bits int, mode string) string {
	c := fe.v(r)
	cur := "i32"
	if mode == "wrapping" {
		m := fe.next("t")
		fe.emit("%s = and i32 %s, %d", m, c, bits-1)
		c = m
	}
	if lt == cur {
		return c
	}
	out := fe.next("t")
	if bits < 32 {
		fe.emit("%s = trunc i32 %s to %s", out, c, lt)
	} else {
		fe.emit("%s = zext i32 %s to %s", out, c, lt)
	}
	return out
}

func (fe *fnEmit) checkOverflow(i *ir.CheckOverflow) {
	t := i.L.Type
	bits, signed := intBits(t)
	if bits == 0 {
		return
	}
	lt := fe.e.lty(t)
	l, r := fe.v(i.L), fe.v(i.R)
	switch i.Op {
	case "+", "-", "*":
		name := map[string]string{"+": "add", "-": "sub", "*": "mul"}[i.Op]
		pre := "u"
		if signed {
			pre = "s"
		}
		intr := fmt.Sprintf("llvm.%s%s.with.overflow.%s", pre, name, lt)
		fe.e.declare(intr, fmt.Sprintf("{ %s, i1 } (%s, %s)", lt, lt, lt))
		res := fe.next("t")
		fe.emit("%s = call { %s, i1 } @%s(%s %s, %s %s)", res, lt, intr, lt, l, lt, r)
		ov := fe.next("t")
		fe.emit("%s = extractvalue { %s, i1 } %s, 1", ov, lt, res)
		fe.check(ov, "overflow", i.Pos)
	case "/", "%":
		z := fe.next("t")
		fe.emit("%s = icmp eq %s %s, 0", z, lt, r)
		fe.check(z, "div_zero", i.Pos)
		if signed {
			minv := fmt.Sprintf("%d", -(int64(1) << uint(bits-1)))
			a := fe.next("t")
			fe.emit("%s = icmp eq %s %s, %s", a, lt, l, minv)
			b := fe.next("t")
			fe.emit("%s = icmp eq %s %s, -1", b, lt, r)
			c := fe.next("t")
			fe.emit("%s = and i1 %s, %s", c, a, b)
			fe.check(c, "overflow", i.Pos)
		}
	case "<<", ">>":
		big := fe.next("t")
		fe.emit("%s = icmp uge i32 %s, %d", big, r, bits)
		fe.check(big, "shift", i.Pos)
		if i.Op == "<<" {
			c := fe.shiftCount(i.R, lt, bits, "")
			s := fe.next("t")
			fe.emit("%s = shl %s %s, %s", s, lt, l, c)
			back := fe.next("t")
			if _, signed := intBits(t); signed {
				fe.emit("%s = ashr %s %s, %s", back, lt, s, c)
			} else {
				fe.emit("%s = lshr %s %s, %s", back, lt, s, c)
			}
			ne := fe.next("t")
			fe.emit("%s = icmp ne %s %s, %s", ne, lt, back, l)
			fe.check(ne, "overflow", i.Pos)
		}
	}
}

func (fe *fnEmit) checkConv(i *ir.CheckConv) {
	src := i.X.Type
	to := &resolve.Prim{Name: i.To}
	x := fe.v(i.X)
	if resolve.IsFloat(src) {
		if resolve.IsFloat(to) {
			return
		}
		st := fe.e.lty(src)
		nan := fe.next("t")
		fe.emit("%s = fcmp uno %s %s, %s", nan, st, x, x)
		fe.check(nan, "conversion", i.Pos)
		tb, tsigned := intBits(to)
		var lo, hi float64
		if tsigned {
			lo, hi = -ldexp(tb-1), ldexp(tb-1)
		} else {
			lo, hi = 0, ldexp(tb)
		}
		tr := fe.next("t")
		fe.e.declare("llvm.trunc."+fltName(st), st+" ("+st+")")
		fe.emit("%s = call %s @llvm.trunc.%s(%s %s)", tr, st, fltName(st), st, x)
		c1 := fe.next("t")
		fe.emit("%s = fcmp olt %s %s, %s", c1, st, tr, fconst(src, lo))
		c2 := fe.next("t")
		fe.emit("%s = fcmp oge %s %s, %s", c2, st, tr, fconst(src, hi))
		c := fe.next("t")
		fe.emit("%s = or i1 %s, %s", c, c1, c2)
		fe.check(c, "conversion", i.Pos)
		return
	}
	sb, ssigned := intBits(src)
	if resolve.IsFloat(to) || sb == 0 {
		return
	}
	tb, tsigned := intBits(to)
	// widen the source to i64 (sign- or zero-extended) and compare against the target range
	w := x
	if sb < 64 {
		w = fe.next("t")
		if ssigned {
			fe.emit("%s = sext %s %s to i64", w, fe.e.lty(src), x)
		} else {
			fe.emit("%s = zext %s %s to i64", w, fe.e.lty(src), x)
		}
	}
	var conds []string
	if tsigned {
		lo := -(int64(1) << uint(tb-1))
		hi := int64(math.MaxInt64)
		if tb < 64 {
			hi = (int64(1) << uint(tb-1)) - 1
		}
		switch {
		case ssigned && tb < 64:
			c1 := fe.next("t")
			fe.emit("%s = icmp slt i64 %s, %d", c1, w, lo)
			c2 := fe.next("t")
			fe.emit("%s = icmp sgt i64 %s, %d", c2, w, hi)
			conds = append(conds, c1, c2)
		case !ssigned:
			// an unsigned source must not exceed the signed maximum (also for i64)
			c2 := fe.next("t")
			fe.emit("%s = icmp ugt i64 %s, %d", c2, w, hi)
			conds = append(conds, c2)
		}
	} else {
		if ssigned {
			c1 := fe.next("t")
			fe.emit("%s = icmp slt i64 %s, 0", c1, w)
			conds = append(conds, c1)
		}
		if tb < 64 {
			hi := (int64(1) << uint(tb)) - 1
			c2 := fe.next("t")
			fe.emit("%s = icmp ugt i64 %s, %d", c2, w, hi)
			conds = append(conds, c2)
		}
	}
	if len(conds) == 0 {
		return
	}
	c := conds[0]
	for _, more := range conds[1:] {
		n := fe.next("t")
		fe.emit("%s = or i1 %s, %s", n, c, more)
		c = n
	}
	fe.check(c, "conversion", i.Pos)
}

// fconst renders a floating-point constant exactly: LLVM hexadecimal
// constants carry the IEEE bits (a float constant is the double that holds
// its exact value).
func fconst(t resolve.Type, v float64) string {
	if p, ok := t.(*resolve.Prim); ok && p.Name == "f32" {
		v = float64(float32(v))
	}
	return fmt.Sprintf("0x%016X", math.Float64bits(v))
}

func ldexp(e int) float64 {
	v := 1.0
	for k := 0; k < e; k++ {
		v *= 2
	}
	return v
}

func fltName(lt string) string {
	if lt == "float" {
		return "f32"
	}
	return "f64"
}

func (fe *fnEmit) conv(i *ir.Conv) {
	src := i.X.Type
	to := &resolve.Prim{Name: i.To}
	st, tt := fe.e.lty(src), fe.e.lty(to)
	x := fe.v(i.X)
	dst := fe.v(i.Dst)
	switch {
	case resolve.IsFloat(src) && resolve.IsFloat(to):
		if st == tt {
			fe.vals[i.Dst] = x
		} else if tt == "double" {
			fe.emit("%s = fpext float %s to double", dst, x)
		} else {
			fe.emit("%s = fptrunc double %s to float", dst, x)
		}
	case resolve.IsFloat(src):
		_, tsigned := intBits(to)
		if i.Saturate {
			fe.e.declare(fmt.Sprintf("llvm.fpto%si.sat.%s.%s", map[bool]string{true: "s", false: "u"}[tsigned], tt, fltName(st)), tt+" ("+st+")")
			fe.emit("%s = call %s @llvm.fpto%si.sat.%s.%s(%s %s)", dst, tt, map[bool]string{true: "s", false: "u"}[tsigned], tt, fltName(st), st, x)
			return
		}
		if tsigned {
			fe.emit("%s = fptosi %s %s to %s", dst, st, x, tt)
		} else {
			fe.emit("%s = fptoui %s %s to %s", dst, st, x, tt)
		}
	case resolve.IsFloat(to):
		_, ssigned := intBits(src)
		if ssigned {
			fe.emit("%s = sitofp %s %s to %s", dst, st, x, tt)
		} else {
			fe.emit("%s = uitofp %s %s to %s", dst, st, x, tt)
		}
	default:
		sb, ssigned := intBits(src)
		tb, _ := intBits(to)
		switch {
		case sb == tb:
			fe.vals[i.Dst] = x
		case sb > tb:
			fe.emit("%s = trunc %s %s to %s", dst, st, x, tt)
		case ssigned:
			fe.emit("%s = sext %s %s to %s", dst, st, x, tt)
		default:
			fe.emit("%s = zext %s %s to %s", dst, st, x, tt)
		}
	}
}

// ---- terminators ----

func (fe *fnEmit) term(t ir.Term) {
	switch x := t.(type) {
	case *ir.Br:
		fe.emit("br label %%%s", fe.labels[x.Target])
	case *ir.CondBr:
		fe.emit("br i1 %s, label %%%s, label %%%s", fe.v(x.Cond), fe.labels[x.Then], fe.labels[x.Else])
	case *ir.Switch:
		keys := make([]int, 0, len(x.Cases))
		for k := range x.Cases {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("i32 %d, label %%%s", k, fe.labels[x.Cases[k]]))
		}
		fe.emit("switch i32 %s, label %%%s [ %s ]", fe.v(x.Tag), fe.labels[x.Default], strings.Join(parts, " "))
	case *ir.Ret:
		rt := fe.e.retType(fe.f)
		if rt == "void" {
			fe.emit("ret void")
			return
		}
		if len(x.Vals) == 1 {
			fe.emit("ret %s %s", rt, fe.v(x.Vals[0]))
			return
		}
		cur := "undef"
		for k, val := range x.Vals {
			r := fe.next("t")
			fe.emit("%s = insertvalue %s %s, %s %s, %d", r, rt, cur, fe.e.lty(val.Type), fe.v(val), k)
			cur = r
		}
		fe.emit("ret %s %s", rt, cur)
	case *ir.Unreachable:
		fe.emit("unreachable")
	}
}
