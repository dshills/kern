package resolve

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"strings"
)

// call types a call expression: plain, generic, method, static, variant
// constructor, intrinsic (thread.spawn / Scope.spawn) or function value.
// isUnsafeOp reports the prelude's enumerated unsafe operations (§6): raw
// pointer operations, inline assembly, and allocator internals.
func isUnsafeOp(f *FuncDecl) bool {
	if f.Module != "core" {
		return false
	}
	if f.Receiver == nil {
		return strings.HasPrefix(f.Name, "ptr_") || f.Name == "asm"
	}
	return f.Receiver.Name == "SystemAllocator" && (f.Name == "alloc" || f.Name == "realloc" || f.Name == "alloc_zeroed" || f.Name == "free")
}

// wrappingIntrinsics are the prelude's wrapping arithmetic forms (§4.1).
var wrappingIntrinsics = map[string]bool{"wrapping_add": true, "wrapping_sub": true, "wrapping_mul": true, "wrapping_neg": true, "wrapping_shl": true, "wrapping_shr": true,
	"saturating_add": true, "saturating_sub": true, "saturating_mul": true,
	"checked_add": true, "checked_sub": true, "checked_mul": true, "checked_div": true, "checked_rem": true, "checked_shl": true, "checked_shr": true}

// bitCountIntrinsics are the prelude's bit-count operations, integer-only
// like the wrapping forms.
var bitCountIntrinsics = map[string]bool{"count_trailing_zeros": true, "count_leading_zeros": true, "count_ones": true}

// loadIntrinsics are the prelude's byte-order reads and writes, integer-only too.
var loadIntrinsics = map[string]bool{"load_be": true, "load_le": true, "store_be": true, "store_le": true}

func (c *fnCtx) call(sc *scope, x *ast.CallExpr, want Type) Type {
	info := &CallInfo{}
	c.r.prog.Info.Calls[x] = info
	switch fn := x.Fn.(type) {
	case *ast.Ident:
		if fn.Name == "len" && sc.lookup("len") == nil && c.m.Funcs["len"] == nil {
			return c.lenBuiltin(sc, x, info)
		}
		if fn.Name == "simd_reduce_add" && sc.lookup(fn.Name) == nil && c.m.Funcs[fn.Name] == nil {
			return c.reduceBuiltin(sc, x, info)
		}
		if sc.lookup(fn.Name) == nil {
			if f, ok := c.m.Funcs[fn.Name]; ok {
				c.r.prog.Info.Idents[fn] = &Symbol{Kind: SymFunc, Name: fn.Name, Func: f}
				return c.callFunc(sc, x, info, f, nil, nil, want)
			}
			if td, ok := c.m.Types[fn.Name]; ok && td.Kind == KindUnion {
				c.errorf("NAME_UNRESOLVED", fn.Pos, "%s is a union; construct a variant", fn.Name)
				return &Bad{}
			}
			var found *TypeDecl
			var vd *VariantDecl
			count := 0
			for _, td := range c.m.Types {
				if td.Kind != KindUnion {
					continue
				}
				for _, v := range td.Variants {
					if v.Name == fn.Name {
						found, vd = td, v
						count++
					}
				}
			}
			if count == 1 {
				c.r.prog.Info.Idents[fn] = &Symbol{Kind: SymType, Name: found.Name, TDecl: found}
				return c.variantCtor(sc, x.Pos, found, vd, nil, want, x.Args)
			}
			if count > 1 {
				c.errorf("NAME_UNRESOLVED", fn.Pos, "variant %s is ambiguous; qualify it with the union name", fn.Name)
				return &Bad{}
			}
		}
	case *ast.IndexExpr:
		if id, ok := fn.X.(*ast.Ident); ok && sc.lookup(id.Name) == nil {
			if f, ok := c.m.Funcs[id.Name]; ok {
				c.r.prog.Info.Idents[id] = &Symbol{Kind: SymFunc, Name: id.Name, Func: f}
				args := c.typeArgsOf(sc, fn.Args)
				return c.callFunc(sc, x, info, f, nil, args, want)
			}
		}
	case *ast.FieldExpr:
		// Static member: Type.method(...), Type[Args].Variant(...), module.func(...)
		if td, explicit, ok := c.typeExprOf(sc, fn.X); ok {
			for _, v := range td.Variants {
				if v.Name == fn.Name {
					return c.variantCtor(sc, x.Pos, td, v, explicit, want, x.Args)
				}
			}
			if td.Name == "thread" && fn.Name == "spawn" {
				return c.spawn(sc, x, info, "thread.spawn", want)
			}
			m := td.Methods[fn.Name]
			if m == nil {
				c.errorf("NAME_UNRESOLVED", fn.Pos, "%s has no function %s", td.Name, fn.Name)
				return &Bad{}
			}
			if len(m.Params) > 0 && m.Params[0].Self {
				c.errorf("TYPE_MISMATCH", fn.Pos, "%s.%s is a method; call it on a value", td.Name, fn.Name)
				return &Bad{}
			}
			var recvArgs map[string]Type
			if explicit != nil {
				recvArgs = map[string]Type{}
				for i, tp := range td.TypeParams {
					if i < len(explicit) {
						recvArgs[tp.Name] = explicit[i]
					}
				}
			}
			return c.callFunc(sc, x, info, m, recvArgs, nil, want)
		}
		if id, ok := fn.X.(*ast.Ident); ok && sc.lookup(id.Name) == nil {
			if mod, ok := c.m.Imports[id.Name]; ok {
				f, ok := mod.Funcs[fn.Name]
				if !ok {
					c.errorf("NAME_UNRESOLVED", fn.Pos, "module %s has no function %s", id.Name, fn.Name)
					return &Bad{}
				}
				c.r.prog.Info.Idents[id] = &Symbol{Kind: SymModule, Name: id.Name, Module: mod}
				if !isExported(fn.Name) && mod.Name != "core" {
					c.errorf("NAME_PRIVATE", fn.Pos, "%s.%s is private to module %s", id.Name, fn.Name, mod.Name)
				}
				return c.callFunc(sc, x, info, f, nil, nil, want)
			}
		}
		// Method call on a value.
		recvT := c.expr(sc, fn.X, nil)
		vt := valueOf(recvT)
		var td *TypeDecl
		var recvArgs map[string]Type
		switch t := vt.(type) {
		case *Named:
			td = t.Decl
			recvArgs = typeArgs(t)
			if td.Name == "Scope" && td.Module == "core" && fn.Name == "spawn" {
				return c.spawn(sc, x, info, "scope.spawn", want)
			}
		case *Prim:
			td = c.r.core.Types["prim_"+t.Name]
		case *TypeParamType:
			for _, cs := range t.Constraints {
				if cs.Iface != nil {
					if im, ok := cs.Iface.Methods[fn.Name]; ok {
						info.Method = true
						c.checkReceiver(sc, fn, im.Params[0].Conv, recvT)
						return c.callFunc(sc, x, info, im, map[string]Type{"Self": t}, nil, want)
					}
				}
			}
			c.errorf("CONSTRAINT_REQUIRED", fn.Pos, "type parameter %s has no interface constraint providing %s", t.Name, fn.Name)
			return &Bad{}
		}
		if td == nil {
			c.errorf("NAME_UNRESOLVED", fn.Pos, "%s has no method %s", typeString(vt), fn.Name)
			return &Bad{}
		}
		m := td.Methods[fn.Name]
		if m == nil {
			if fd, _ := FieldType(&Named{Decl: td}, fn.Name); fd != nil {
				// calling a function-pointer field
				ft := c.field(sc, fn)
				c.r.prog.Info.Types[fn] = ft
				return c.callValue(sc, x, info, valueOf(ft))
			}
			c.errorf("NAME_UNRESOLVED", fn.Pos, "%s has no method %s", td.Name, fn.Name)
			if s := nearest(fn.Name, keys(td.Methods)); s != "" {
				// the field expression spans the receiver too; the name is its tail
				c.fix(diag.Replace(c.file, diag.Span{Start: fn.End - len(fn.Name), End: fn.End}, s))
			}
			return &Bad{}
		}
		if len(m.Params) == 0 || !m.Params[0].Self {
			c.errorf("TYPE_MISMATCH", fn.Pos, "%s.%s is not a method", td.Name, fn.Name)
			return &Bad{}
		}
		info.Method = true
		if recvArgs == nil {
			recvArgs = map[string]Type{}
		}
		recvArgs["Self"] = vt
		c.checkReceiver(sc, fn, m.Params[0].Conv, recvT)
		return c.callFunc(sc, x, info, m, recvArgs, nil, want)
	}
	// Call through a function value.
	ft := c.expr(sc, x.Fn, nil)
	return c.callValue(sc, x, info, valueOf(ft))
}

func (c *fnCtx) callValue(sc *scope, x *ast.CallExpr, info *CallInfo, ft Type) Type {
	f, ok := ft.(*Func)
	if !ok {
		c.errorf("TYPE_MISMATCH", x.Fn.Span(), "%s is not callable", typeString(ft))
		return &Bad{}
	}
	info.Params = f.Params
	info.Ret = f.Ret
	c.checkArgs(sc, x, info, f.Params, x.Args, nil, nil)
	return retType(f.Ret)
}

func retType(ret []Type) Type {
	switch len(ret) {
	case 0:
		return &Unit{}
	case 1:
		return ret[0]
	}
	return &Tuple{Elems: ret}
}

// callFunc types a call to a declared function or method. recvArgs binds the
// receiver type's parameters (and Self); explicit are explicit function type
// arguments.
func (c *fnCtx) callFunc(sc *scope, x *ast.CallExpr, info *CallInfo, f *FuncDecl, recvArgs map[string]Type, explicit []Type, want Type) Type {
	info.Fn = f
	info.Release = f.Release != ""
	info.MultiAccess = len(f.Ret) > 1
	m := map[string]Type{}
	for k, v := range recvArgs {
		m[k] = v
	}
	params := map[string]bool{}
	for _, tp := range f.TypeParams {
		params[tp.Name] = true
	}
	for _, tp := range f.OwnerParams {
		if _, bound := m[tp.Name]; !bound {
			params[tp.Name] = true
		}
	}
	if explicit != nil {
		if len(explicit) != len(f.TypeParams) {
			c.errorf("TYPE_ARITY", x.Pos, "%s expects %d type arguments, got %d", f.Name, len(f.TypeParams), len(explicit))
		}
		for i, tp := range f.TypeParams {
			if i < len(explicit) {
				m[tp.Name] = explicit[i]
				c.r.checkConstraints(c.file, x.Fn.Span(), explicit[i], tp)
			}
		}
	}
	// Infer from the expected type when the return type mentions parameters.
	if want != nil && len(f.Ret) == 1 {
		Unify(f.Ret[0], valueOf(want), params, m)
	}
	var fparams []*ParamDecl
	var args []*ast.Arg
	if info.Method {
		fparams = f.Params[1:]
		args = x.Args
	} else {
		fparams = f.Params
		args = x.Args
	}
	fps := make([]FuncParam, len(fparams))
	for i, p := range fparams {
		fps[i] = FuncParam{Conv: p.Conv, Type: p.Type}
	}
	c.checkArgs(sc, x, info, fps, args, params, m)
	if strings.HasPrefix(f.Name, "simd_") && f.Module == "core" {
		c.checkSIMD(x, f, m)
	}
	if (wrappingIntrinsics[f.Name] || bitCountIntrinsics[f.Name] || loadIntrinsics[f.Name]) && f.Module == "core" {
		if t, ok := m["T"]; ok && !IsInteger(t) {
			c.errorf("TYPE_MISMATCH", x.Pos, "%s requires an integer type, found %s", f.Name, typeString(t))
		}
	}
	// Vec.extend duplicates its source elements bitwise, so the element type
	// must be Copy. A method cannot constrain its owner's type parameter
	// (conditional implementations are not supported), so the requirement is
	// checked at the call, like the integer-only forms above.
	if f.Module == "core" && f.Receiver != nil && f.Receiver.Name == "Vec" && f.Name == "extend" {
		if t, ok := m["T"]; ok && !c.r.satisfies(t, &Constraint{Marker: "Copy"}) {
			c.errorf("CONSTRAINT_UNSATISFIED", x.Pos, "Vec.extend requires a Copy element type, found %s", typeString(t))
		}
	}
	// constraints on inferred parameters (the function's own, and the owner's
	// when they were not bound by the receiver or explicit arguments)
	for _, tp := range f.TypeParams {
		t, ok := m[tp.Name]
		if !ok {
			c.errorf("TYPE_INFER_FAILED", x.Pos, "cannot infer type parameter %s of %s", tp.Name, f.Name)
			m[tp.Name] = &Bad{}
			continue
		}
		if explicit == nil {
			c.r.checkConstraints(c.file, x.Pos, t, tp)
		}
	}
	if f.Name == "zero" && f.Module == "core" {
		if zt, ok := m["T"]; ok && !IsZeroable(zt) {
			c.errorf("CONSTRAINT_UNSATISFIED", x.Pos, "%s is not zeroable (it contains a function type)", typeString(zt))
		}
	}
	for _, tp := range f.OwnerParams {
		if !params[tp.Name] {
			continue
		}
		t, ok := m[tp.Name]
		if !ok {
			c.errorf("TYPE_INFER_FAILED", x.Pos, "cannot infer type parameter %s; write the receiver type explicitly, e.g. %s[...]", tp.Name, receiverName(f))
			m[tp.Name] = &Bad{}
			continue
		}
		c.r.checkConstraints(c.file, x.Pos, t, tp)
	}
	info.TypeArgs = m
	var ps []FuncParam
	if info.Method {
		ps = append(ps, FuncParam{Conv: f.Params[0].Conv, Type: Subst(f.Params[0].Type, m)})
	}
	for _, p := range fps {
		ps = append(ps, FuncParam{Conv: p.Conv, Type: Subst(p.Type, m)})
	}
	info.Params = ps
	for _, r := range f.Ret {
		info.Ret = append(info.Ret, Subst(r, m))
	}
	if f.Extern != "" && (f.AST == nil || f.AST.Body == nil) && c.unsafe == 0 {
		c.errorf("UNSAFE_OP_OUTSIDE_UNSAFE", x.Pos, "calling extern function %s requires unsafe", f.Name)
	}
	if c.unsafe == 0 && isUnsafeOp(f) {
		c.errorf("UNSAFE_OP_OUTSIDE_UNSAFE", x.Pos, "%s is an unsafe operation and requires unsafe", f.Name)
	}
	return retType(info.Ret)
}

// checkArgs checks arguments against parameter conventions and types,
// unifying generic parameters when params/m are given.
func (c *fnCtx) checkArgs(sc *scope, x *ast.CallExpr, info *CallInfo, fps []FuncParam, args []*ast.Arg, params map[string]bool, m map[string]Type) {
	if len(args) != len(fps) {
		c.errorf("CALL_ARITY", x.Pos, "expected %d arguments, got %d", len(fps), len(args))
	}
	for i, a := range args {
		if i >= len(fps) {
			c.expr(sc, a.X, nil)
			continue
		}
		p := fps[i]
		var wantArg Type
		if m == nil {
			wantArg = p.Type
		} else if sub := Subst(p.Type, m); !mentionsUnbound(sub, params, m) {
			wantArg = sub
		}
		pt := valueOf(wantArg)
		if pa, ok := p.Type.(*Access); ok && pa.Seq {
			if wantArg != nil {
				pt = &Slice{Elem: valueOf(wantArg).(*Slice).Elem}
			}
		}
		switch p.Conv {
		case ast.ConvOut:
			if !a.Out {
				c.errorf("TYPE_MISMATCH", a.Pos, "argument %d must be written out <ident>", i+1)
				c.expr(sc, a.X, nil)
				continue
			}
			id, ok := a.X.(*ast.Ident)
			if !ok {
				c.errorf("PLACE_REQUIRED", a.Pos, "out argument must be a local")
				continue
			}
			sym := sc.lookup(id.Name)
			if sym == nil || (sym.Kind != SymLocal && (sym.Kind != SymParam || sym.Conv != ast.ConvOut)) {
				c.errorf("PLACE_REQUIRED", a.Pos, "out argument must name an uninitialized local or out parameter")
				continue
			}
			c.r.prog.Info.Idents[id] = sym
			c.r.prog.Info.Types[id] = sym.Type
			c.unifyArg(a.X, p.Type, sym.Type, params, m)
		case ast.ConvView, ast.ConvEdit:
			if a.Out {
				c.errorf("TYPE_MISMATCH", a.Pos, "out is only valid for out parameters")
			}
			at := c.expr(sc, a.X, pt)
			if acc, ok := at.(*Access); ok {
				if p.Conv == ast.ConvEdit && !acc.Edit {
					c.errorf("ACC_PERMISSION_ESCALATION", a.Pos, "edit parameter given a view")
				}
				c.unifyArg(a.X, accessElemPattern(p.Type), accessElem(acc), params, m)
				continue
			}
			if !c.isPlace(sc, a.X) {
				c.errorf("PLACE_REQUIRED", a.Pos, "%s parameter requires a place", convName(p.Conv))
				continue
			}
			if p.Conv == ast.ConvEdit && !c.placeIsMutable(sc, a.X) {
				c.errorf("ACC_PERMISSION_ESCALATION", a.Pos, "edit parameter given a read-only place")
			}
			// An array place coerces to a sequence access.
			if pa, ok := p.Type.(*Access); ok && pa.Seq {
				if arr, ok := at.(*Array); ok {
					at = &Slice{Elem: arr.Elem}
				}
			}
			c.unifyArg(a.X, accessElemPattern(p.Type), at, params, m)
		default: // own or by-value
			if a.Out {
				c.errorf("TYPE_MISMATCH", a.Pos, "out is only valid for out parameters")
			}
			at := c.expr(sc, a.X, pt)
			if acc, ok := at.(*Access); ok {
				if !IsCopy(accessElem(acc)) {
					c.errorf("TYPE_MISMATCH", a.Pos, "an access cannot be passed by value; use copy or an owned value")
				}
				at = accessElem(acc)
			}
			c.unifyArg(a.X, p.Type, at, params, m)
		}
	}
}

func convName(cv ast.Conv) string {
	switch cv {
	case ast.ConvView:
		return "view"
	case ast.ConvEdit:
		return "edit"
	case ast.ConvOwn:
		return "own"
	case ast.ConvOut:
		return "out"
	}
	return "value"
}

// accessElemPattern returns the element pattern of an access parameter type:
// view[]T -> []T, view[T] -> T.
func accessElemPattern(t Type) Type {
	if a, ok := t.(*Access); ok {
		return accessElem(a)
	}
	return t
}

func (c *fnCtx) unifyArg(e ast.Expr, pattern, actual Type, params map[string]bool, m map[string]Type) {
	if m == nil {
		if !Identical(pattern, actual) {
			c.errorf("TYPE_MISMATCH", e.Span(), "expected %s, found %s", typeString(pattern), typeString(actual))
		}
		return
	}
	if !Unify(pattern, actual, params, m) {
		c.errorf("TYPE_MISMATCH", e.Span(), "expected %s, found %s", typeString(Subst(pattern, m)), typeString(actual))
	}
}

// spawn types thread.spawn(f, args...) and s.spawn(f, args...) (spec §5.5).
func (c *fnCtx) spawn(sc *scope, x *ast.CallExpr, info *CallInfo, kind string, want Type) Type {
	info.Intrinsic = kind
	if len(x.Args) == 0 {
		c.errorf("CALL_ARITY", x.Pos, "spawn requires a function argument")
		return &Bad{}
	}
	ft, ok := valueOf(c.expr(sc, x.Args[0].X, nil)).(*Func)
	if !ok {
		c.errorf("TYPE_MISMATCH", x.Args[0].Pos, "spawn requires a function value")
		return &Bad{}
	}
	if kind == "thread.spawn" {
		for _, p := range ft.Params {
			if p.Conv == ast.ConvView || p.Conv == ast.ConvEdit || p.Conv == ast.ConvOut {
				c.errorf("ACC_LIVE_ACROSS_SPAWN", x.Args[0].Pos, "thread.spawn requires a function whose parameters are all own")
			}
		}
	}
	info.Params = append([]FuncParam{{Conv: ast.ConvValue, Type: ft}}, ft.Params...)
	c.checkArgs(sc, x, info, ft.Params, x.Args[1:], nil, nil)
	// Send/Share requirements
	for i, p := range ft.Params {
		if i+1 >= len(x.Args) {
			break
		}
		a := x.Args[i+1]
		switch p.Conv {
		case ast.ConvView:
			if !IsShare(valueOf(p.Type)) {
				c.errorf("SHARE_NOT_SHARE", a.Pos, "%s is viewed from another thread but is not Share", typeString(valueOf(p.Type)))
			}
		default:
			if !IsSend(valueOf(p.Type)) {
				c.errorf("SEND_NOT_SEND", a.Pos, "%s is moved to another thread but is not Send", typeString(valueOf(p.Type)))
			}
		}
	}
	r := retType(ft.Ret)
	if !IsSend(r) {
		c.errorf("SEND_NOT_SEND", x.Pos, "thread result %s is not Send", typeString(r))
	}
	wrapper := "Thread"
	if kind == "scope.spawn" {
		wrapper = "Join"
	}
	rt := &Named{Decl: c.r.core.Types[wrapper], Args: []Type{r}}
	res := &Named{Decl: c.r.core.Types["Result"], Args: []Type{rt, &Named{Decl: c.r.core.Types["SpawnErr"]}}}
	info.Ret = []Type{res}
	return res
}

// checkReceiver applies the receiver convention to the receiver expression:
// an edit receiver needs a mutable place or an edit access; an own receiver
// needs an owned value.
func (c *fnCtx) checkReceiver(sc *scope, fn *ast.FieldExpr, conv ast.Conv, recvT Type) {
	switch conv {
	case ast.ConvEdit:
		if acc, ok := recvT.(*Access); ok {
			if !acc.Edit {
				c.errorf("ACC_PERMISSION_ESCALATION", fn.X.Span(), "edit receiver given a view")
			}
			return
		}
		if !c.isPlace(sc, fn.X) {
			c.errorf("PLACE_REQUIRED", fn.X.Span(), "edit receiver requires a place")
			return
		}
		if !c.placeIsMutable(sc, fn.X) {
			c.errorf("ACC_PERMISSION_ESCALATION", fn.X.Span(), "edit receiver given a read-only place")
		}
	case ast.ConvOwn:
		if acc, ok := recvT.(*Access); ok && !IsCopy(accessElem(acc)) {
			c.errorf("TYPE_MISMATCH", fn.X.Span(), "own receiver cannot be an access")
		}
	}
}

// simdVector reports whether t is a SIMD vector type [N]E (N in 2, 4, 8,
// 16; E a number) and returns its element type and length.
func simdVector(t Type) (Type, int, bool) {
	a, ok := t.(*Array)
	if !ok || (a.Len != 2 && a.Len != 4 && a.Len != 8 && a.Len != 16) || (!IsInteger(a.Elem) && !IsFloat(a.Elem)) {
		return nil, 0, false
	}
	return a.Elem, a.Len, true
}

// checkSIMD validates the type arguments of a simd_* intrinsic.
func (c *fnCtx) checkSIMD(x *ast.CallExpr, f *FuncDecl, m map[string]Type) {
	t, ok := m["T"]
	if !ok {
		return
	}
	elem, n, isVec := simdVector(t)
	if !isVec {
		c.errorf("TYPE_MISMATCH", x.Pos, "%s requires a vector [N]E with N in 2, 4, 8, 16 and a numeric E, found %s", f.Name, typeString(t))
		return
	}
	switch f.Name {
	case "simd_and", "simd_or", "simd_xor":
		if !IsInteger(elem) {
			c.errorf("TYPE_MISMATCH", x.Pos, "%s requires integer lanes, found %s", f.Name, typeString(elem))
		}
	case "simd_shuffle":
		it, ok := m["I"]
		ia, isArr := it.(*Array)
		if !ok || !isArr || ia.Len != n || !Identical(ia.Elem, &Prim{Name: "u32"}) {
			c.errorf("TYPE_MISMATCH", x.Pos, "simd_shuffle requires an index vector [%d]u32", n)
		}
	}
}

// reduceBuiltin types simd_reduce_add(v) as the lane type of v.
func (c *fnCtx) reduceBuiltin(sc *scope, x *ast.CallExpr, info *CallInfo) Type {
	info.Intrinsic = "simd_reduce_add"
	if len(x.Args) != 1 {
		c.errorf("CALL_ARITY", x.Pos, "simd_reduce_add takes one argument")
		return &Bad{}
	}
	at := valueOf(c.expr(sc, x.Args[0].X, nil))
	elem, _, ok := simdVector(at)
	if !ok {
		c.errorf("TYPE_MISMATCH", x.Args[0].Pos, "simd_reduce_add requires a vector, found %s", typeString(at))
		return &Bad{}
	}
	info.TypeArgs = map[string]Type{"T": at}
	info.Params = []FuncParam{{Conv: ast.ConvValue, Type: at}}
	info.Ret = []Type{elem}
	return elem
}

// lenBuiltin types len(x) for arrays, slices and sequence accesses (spec §5.2).
func (c *fnCtx) lenBuiltin(sc *scope, x *ast.CallExpr, info *CallInfo) Type {
	info.Intrinsic = "len"
	if len(x.Args) != 1 {
		c.errorf("CALL_ARITY", x.Pos, "len takes one argument")
		return &Prim{Name: "usize"}
	}
	if x.Args[0].Out {
		c.errorf("TYPE_MISMATCH", x.Args[0].Pos, "out is only valid for out parameters")
	}
	at := c.expr(sc, x.Args[0].X, nil)
	var elem Type
	switch t := at.(type) {
	case *Array:
		elem = t.Elem
	case *Slice:
		elem = t.Elem
	case *Access:
		if t.Seq {
			elem = t.Elem
		} else if arr, ok := t.Elem.(*Array); ok {
			elem = arr.Elem
		}
	}
	if elem == nil {
		c.errorf("TYPE_MISMATCH", x.Args[0].Pos, "len requires an array or sequence, found %s", typeString(at))
		return &Prim{Name: "usize"}
	}
	info.Params = []FuncParam{{Conv: ast.ConvView, Type: &Access{Seq: true, Elem: elem}}}
	info.Ret = []Type{&Prim{Name: "usize"}}
	return &Prim{Name: "usize"}
}

func receiverName(f *FuncDecl) string {
	if f.Receiver != nil {
		return f.Receiver.Name
	}
	return f.Name
}
