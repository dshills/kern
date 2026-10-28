// Package lower translates the resolved, checked AST into the semantic IR
// (spec §7), specializing generic functions per instantiation (monomorphic
// IR, PLAN.md 1.1) and inserting the static destruction and access-end
// points recorded by the checker.
package lower

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// preludeFile is the file name the resolver gives the prelude.
const preludeFile = "std/core.kern"

// Lower lowers a resolved program. Generic functions are lowered once per
// instantiation reachable from the main module's non-generic functions.
func Lower(prog *resolve.Program) (mod *ir.Module, diags []diag.Diagnostic) {
	l := &lowerer{prog: prog, info: prog.Info, mod: &ir.Module{Name: prog.Main.Name}, done: map[string]*ir.Func{}, parent: map[string]string{}, dtorType: map[string]*resolve.Named{}}
	defer func() {
		if r := recover(); r != nil {
			if le, ok := r.(lowerError); ok {
				l.diags = append(l.diags, diag.New("INTERNAL", le.file, diag.Span{Start: le.pos.Start, End: le.pos.End}, "lowering: %s", le.msg))
				mod, diags = l.mod, l.diags
				return
			}
			panic(r)
		}
	}()
	// Roots: every non-generic function and method of the main module and
	// every destructor of a non-generic type.
	for _, f := range prog.Main.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				fd := l.info.Funcs[d]
				if fd != nil && len(fd.TypeParams) == 0 {
					l.request(fd, nil, f.Name)
				}
			case *ast.ImplDecl:
				for _, fn := range d.Funcs {
					fd := l.info.Funcs[fn]
					if fd != nil && len(fd.TypeParams) == 0 && len(fd.OwnerParams) == 0 {
						l.request(fd, nil, f.Name)
					}
				}
				if d.Destructor != nil {
					if self := l.info.Decls[d.Destructor]; self != nil {
						if n, ok := self.Type.(*resolve.Named); ok && len(n.Decl.TypeParams) == 0 {
							l.requestDestructor(n, f.Name)
						}
					}
				}
			}
		}
	}
	l.drain()
	if main := l.mod.Func(prog.Main.Name + ".main"); main != nil {
		l.mod.Entry = main.Name
	}
	return l.mod, l.diags
}

type lowerError struct {
	file string
	pos  ast.Pos
	msg  string
}

type lowerer struct {
	globals map[*resolve.GlobalDecl]*ir.Global
	prog    *resolve.Program
	info    *resolve.Info
	mod     *ir.Module
	done    map[string]*ir.Func
	queue   []*pending
	diags   []diag.Diagnostic
	// cur is the function being lowered; parent records which function
	// first requested each instantiation and dtorType the type whose
	// destructor an instantiation is, for the expanding-recursion check.
	cur      string
	parent   map[string]string
	dtorType map[string]*resolve.Named
	closures int // closure functions lowered so far (their names)
}

type pending struct {
	fd    *resolve.FuncDecl
	subst map[string]resolve.Type
	name  string
	file  string
	dtor  *resolve.Named
}

// mangle builds the IR name of a function instantiation.
func (l *lowerer) mangle(fd *resolve.FuncDecl, subst map[string]resolve.Type) string {
	var sb strings.Builder
	if fd.Module != "" && fd.Receiver == nil {
		sb.WriteString(fd.Module + ".")
	}
	if fd.Receiver != nil {
		sb.WriteString(fd.Receiver.Name + ".")
	}
	sb.WriteString(fd.Name)
	var params []*resolve.TypeParamType
	params = append(params, fd.OwnerParams...)
	params = append(params, fd.TypeParams...)
	if len(params) > 0 {
		var parts []string
		for _, tp := range params {
			if t, ok := subst[tp.Name]; ok {
				parts = append(parts, tp.Name+"="+t.String())
			}
		}
		if len(parts) > 0 {
			sb.WriteString("[" + strings.Join(parts, ",") + "]")
		}
	}
	return sb.String()
}

// global returns the IR global of a thread_local declaration, creating it once.
func (l *lowerer) global(g *resolve.GlobalDecl) *ir.Global {
	if l.globals == nil {
		l.globals = map[*resolve.GlobalDecl]*ir.Global{}
	}
	if x, ok := l.globals[g]; ok {
		return x
	}
	x := &ir.Global{ID: len(l.mod.Globals), Name: g.Module + "." + g.Name, Type: g.Type, Init: g.Init}
	l.globals[g] = x
	l.mod.Globals = append(l.mod.Globals, x)
	return x
}

// request schedules an instantiation and returns its IR name.
func (l *lowerer) request(fd *resolve.FuncDecl, subst map[string]resolve.Type, file string) string {
	name := l.mangle(fd, subst)
	if _, ok := l.done[name]; ok {
		return name
	}
	library := fd.Module == "core" && !fd.Intrinsic
	if library {
		file = preludeFile
	}
	f := &ir.Func{Name: name, Release: fd.Release, Contract: fd.Contract, Pos: fd.Pos, File: file, Extern: fd.Extern, Intrinsic: fd.Intrinsic, Library: library}
	l.done[name] = f
	l.parent[name] = l.cur
	l.mod.Add(f)
	for _, p := range fd.Params {
		f.Params = append(f.Params, &ir.Local{Name: p.Name, Type: resolve.Subst(p.Type, subst), Conv: p.Conv})
	}
	for _, r := range fd.Ret {
		f.Rets = append(f.Rets, resolve.Subst(r, subst))
	}
	if fd.AST != nil && fd.AST.Body != nil && (fd.Module == l.prog.Main.Name || library) {
		l.queue = append(l.queue, &pending{fd: fd, subst: subst, name: name, file: file})
	}
	return name
}

func (l *lowerer) requestDestructor(n *resolve.Named, file string) string {
	name := n.String() + ".destructor"
	if _, ok := l.done[name]; ok {
		return name
	}
	subst := map[string]resolve.Type{}
	for i, tp := range n.Decl.TypeParams {
		subst[tp.Name] = n.Args[i]
	}
	f := &ir.Func{Name: name, Pos: n.Decl.Pos}
	f.Params = []*ir.Local{{Name: "self", Type: n, Conv: ast.ConvEdit}}
	l.done[name] = f
	l.parent[name] = l.cur
	l.dtorType[name] = n
	l.mod.Add(f)
	switch {
	case n.Decl.Destructor != nil && hasKernDestructor(n, l.prog.Main.Name) && l.expanding(n):
		// an expanding recursive instantiation (A[T] whose destructor
		// reaches A[Option[T]], and so on): the chain is cut after
		// maxExpansion growing steps, as marker derivation cuts it. The
		// instantiation beyond the bound is not compiled: destroying a value
		// nested that deeply traps `unreachable` rather than leaking silently
		f.Entry = &ir.Block{Name: "entry", Instrs: []ir.Instr{&ir.Trap{Kind: "unreachable", Pos: n.Decl.Pos}}, Term: &ir.Unreachable{}}
		f.Blocks = []*ir.Block{f.Entry}
		f.Locals = []*ir.Local{f.Params[0]}
	case n.Decl.Destructor != nil && hasKernDestructor(n, l.prog.Main.Name):
		if n.Decl.Module == "core" {
			file = preludeFile
			f.File = file
			f.Library = true
		}
		l.queue = append(l.queue, &pending{name: name, subst: subst, file: file, dtor: n})
	default:
		f.Intrinsic = true
	}
	return name
}

func (l *lowerer) drain() {
	for len(l.queue) > 0 {
		p := l.queue[0]
		l.queue = l.queue[1:]
		f := l.done[p.name]
		l.cur = p.name
		if p.dtor != nil {
			l.lowerDestructor(f, p)
			continue
		}
		l.lowerFunc(f, p)
	}
	l.cur = ""
}

// ---- per-function lowering context ----

type fnLower struct {
	l      *lowerer
	f      *ir.Func
	file   string
	subst  map[string]resolve.Type
	cur    *ir.Block
	locals map[*resolve.Symbol]*ir.Local
	accs   map[*resolve.Symbol]*ir.Value
	relOf  map[*ir.Value]releaseInfo // release obligations of access values
	loops  []*loopCtx
	fd     *resolve.FuncDecl
	frames []*frame
	// closureAccs are the capture accesses opened by closure arguments of
	// the call being lowered; the call ends them with its other arguments
	closureAccs []*ir.Value
}

// frame mirrors one checker scope: the symbols bound in it (whose exits the
// checker lists) and the IR-level temporaries the lowering created for it
// (subject accesses, owned subject temporaries, sequence accesses), which
// must be closed on every exit path after the symbols.
type frame struct {
	syms   map[*resolve.Symbol]bool
	accs   []*ir.Value
	temps  []*ir.Local
	late   []*ir.Value // accesses ended after the temporaries (scoped-spawn arguments, valid until the scope joins)
	isLoop bool
}

// frameOf returns the index of the frame that bound sym, or -1.
func (fl *fnLower) frameOf(sym *resolve.Symbol) int {
	for i := len(fl.frames) - 1; i >= 0; i-- {
		if fl.frames[i].syms[sym] {
			return i
		}
	}
	return -1
}

func (fl *fnLower) pushFrame() *frame {
	fr := &frame{syms: map[*resolve.Symbol]bool{}}
	fl.frames = append(fl.frames, fr)
	return fr
}

func (fl *fnLower) popFrame() { fl.frames = fl.frames[:len(fl.frames)-1] }

func (fl *fnLower) top() *frame { return fl.frames[len(fl.frames)-1] }

// bindSym records that sym belongs to the current frame.
func (fl *fnLower) bindSym(sym *resolve.Symbol) {
	if len(fl.frames) > 0 {
		fl.top().syms[sym] = true
	}
}

// exit emits, for the frames from the innermost down to index upto, the
// checker-listed symbol exits belonging to each frame followed by the
// frame's own temporaries. Symbols the lowering never saw are emitted last.
func (fl *fnLower) exit(list []*resolve.Symbol, upto int) {
	seen := map[*resolve.Symbol]bool{}
	for i := len(fl.frames) - 1; i >= upto && i >= 0; i-- {
		fr := fl.frames[i]
		for _, sym := range list {
			if fr.syms[sym] && !seen[sym] {
				seen[sym] = true
				fl.dropSym(sym)
			}
		}
		for j := len(fr.accs) - 1; j >= 0; j-- {
			fl.endAccess(fr.accs[j])
		}
		for j := len(fr.temps) - 1; j >= 0; j-- {
			fl.emitDrop(ir.Place{Local: fr.temps[j]}, fr.temps[j].Type, ast.Pos{})
		}
		for j := len(fr.late) - 1; j >= 0; j-- {
			fl.endAccess(fr.late[j])
		}
	}
	for _, sym := range list {
		if !seen[sym] {
			fl.dropSym(sym)
		}
	}
}

// loopFrame returns the index of the innermost loop frame.
func (fl *fnLower) loopFrame() int {
	for i := len(fl.frames) - 1; i >= 0; i-- {
		if fl.frames[i].isLoop {
			return i
		}
	}
	return 0
}

type releaseInfo struct {
	fn   string
	recv *ir.Value
}

type loopCtx struct {
	breakBlk, continueBlk *ir.Block
}

func (fl *fnLower) fail(pos ast.Pos, format string, args ...any) {
	panic(lowerError{file: fl.file, pos: pos, msg: fmt.Sprintf(format, args...)})
}

func (fl *fnLower) ty(t resolve.Type) resolve.Type { return resolve.Subst(t, fl.subst) }

func (fl *fnLower) typeOf(e ast.Expr) resolve.Type {
	t := fl.l.info.Types[e]
	if t == nil {
		return &resolve.Unit{}
	}
	return fl.ty(t)
}

func (fl *fnLower) newBlock(name string) *ir.Block {
	b := &ir.Block{ID: len(fl.f.Blocks), Name: name}
	fl.f.Blocks = append(fl.f.Blocks, b)
	return b
}

func (fl *fnLower) newValue(t resolve.Type) *ir.Value {
	v := &ir.Value{ID: fl.f.NumValues, Type: t}
	fl.f.NumValues++
	return v
}

func (fl *fnLower) newTemp(t resolve.Type) *ir.Local {
	loc := &ir.Local{ID: len(fl.f.Locals), Name: fmt.Sprintf("t%d", len(fl.f.Locals)), Type: t, Temp: true}
	fl.f.Locals = append(fl.f.Locals, loc)
	return loc
}

func (fl *fnLower) emit(in ir.Instr) {
	if fl.cur == nil {
		fl.cur = fl.newBlock("dead")
	}
	fl.cur.Instrs = append(fl.cur.Instrs, in)
}

func (fl *fnLower) terminate(t ir.Term) {
	if fl.cur != nil && fl.cur.Term == nil {
		fl.cur.Term = t
	}
	fl.cur = nil
}

func (fl *fnLower) local(sym *resolve.Symbol) *ir.Local {
	if loc, ok := fl.locals[sym]; ok {
		return loc
	}
	loc := &ir.Local{ID: len(fl.f.Locals), Name: sym.Name, Type: fl.ty(sym.Type), Conv: sym.Conv}
	if sym.Kind == resolve.SymLocal {
		loc.Conv = ast.ConvValue
	}
	fl.f.Locals = append(fl.f.Locals, loc)
	fl.locals[sym] = loc
	return loc
}

func (l *lowerer) lowerFunc(f *ir.Func, p *pending) {
	fl := &fnLower{l: l, f: f, file: p.file, subst: p.subst, locals: map[*resolve.Symbol]*ir.Local{}, accs: map[*resolve.Symbol]*ir.Value{}, relOf: map[*ir.Value]releaseInfo{}, fd: p.fd}
	entry := fl.newBlock("entry")
	f.Entry = entry
	fl.cur = entry
	fl.pushFrame()
	// parameters are the first locals, bound to their symbols
	for i, pd := range p.fd.Params {
		sym := l.info.Decls[pd]
		loc := f.Params[i]
		loc.ID = len(f.Locals)
		f.Locals = append(f.Locals, loc)
		if sym != nil {
			fl.locals[sym] = loc
			fl.bindSym(sym)
			if pd.Conv == ast.ConvView || pd.Conv == ast.ConvEdit {
				// an access parameter is itself an open access value
				v := fl.newValue(fl.ty(pd.Type))
				fl.emit(&ir.AccessBegin{Dst: v, Edit: pd.Conv == ast.ConvEdit, Place: ir.Place{Local: loc}, Pos: pd.Pos})
				fl.accs[sym] = v
			}
		}
	}
	fl.block(p.fd.AST.Body)
	if fl.cur != nil {
		// fallthrough end: parameter drops recorded by the checker
		fl.exit(l.info.Drops[p.fd.AST], 0)
		if len(f.Rets) == 0 {
			fl.terminate(&ir.Ret{})
		} else {
			fl.terminate(&ir.Unreachable{})
		}
	}
	sortBlocks(f)
}

func (l *lowerer) lowerDestructor(f *ir.Func, p *pending) {
	n := p.dtor
	d := n.Decl.Destructor
	fl := &fnLower{l: l, f: f, file: p.file, subst: p.subst, locals: map[*resolve.Symbol]*ir.Local{}, accs: map[*resolve.Symbol]*ir.Value{}, relOf: map[*ir.Value]releaseInfo{}, fd: &resolve.FuncDecl{Name: "destructor"}}
	entry := fl.newBlock("entry")
	f.Entry = entry
	fl.cur = entry
	loc := f.Params[0]
	loc.ID = 0
	f.Locals = append(f.Locals, loc)
	fl.pushFrame()
	if self := l.info.Decls[d]; self != nil {
		fl.locals[self] = loc
		fl.bindSym(self)
		v := fl.newValue(&resolve.Access{Edit: true, Elem: n})
		fl.emit(&ir.AccessBegin{Dst: v, Edit: true, Place: ir.Place{Local: loc}, Pos: d.Pos})
		fl.accs[self] = v
	}
	fl.block(d.Body)
	if fl.cur != nil {
		fl.terminate(&ir.Ret{})
	}
	sortBlocks(f)
}

func sortBlocks(f *ir.Func) {
	sort.SliceStable(f.Blocks, func(i, j int) bool { return f.Blocks[i].ID < f.Blocks[j].ID })
}

// dropSym emits the access end (with release) or drop for one symbol.
func (fl *fnLower) dropSym(sym *resolve.Symbol) {
	if sym.Kind == resolve.SymAccess {
		if v, ok := fl.accs[sym]; ok {
			fl.endAccess(v)
		}
		return
	}
	if t := fl.ty(sym.Type); resolve.IsCopy(t) || isClosure(t) {
		return // nothing to destroy: a closure parameter is its function and environment
	}
	loc := fl.local(sym)
	fl.emitDrop(ir.Place{Local: loc}, loc.Type, sym.Pos)
}

// isClosure reports a closure type (spec §4): second-class, never owned.
func isClosure(t resolve.Type) bool {
	ft, ok := t.(*resolve.Func)
	return ok && ft.Closure
}

// emitDrop emits a Drop and schedules the destructor of every main-module
// type it may run.
func (fl *fnLower) emitDrop(p ir.Place, t resolve.Type, pos ast.Pos) {
	fl.requestDestructors(t, map[string]bool{})
	fl.emit(&ir.Drop{Place: p, Type: t, Pos: pos})
}

func (fl *fnLower) requestDestructors(t resolve.Type, path map[string]bool) {
	switch x := t.(type) {
	case *resolve.Array:
		fl.requestDestructors(x.Elem, path)
	case *resolve.Named:
		key := x.String()
		if path[key] || expansionDepth(path, x.Decl) > 8 {
			// a cycle, or an expanding recursive instantiation (A[T]
			// containing A[Option[T]]): bounded as in marker derivation
			return
		}
		path[key] = true
		defer delete(path, key)
		if x.Decl.Destructor != nil && hasKernDestructor(x, fl.l.prog.Main.Name) {
			fl.l.requestDestructor(x, fl.file)
		}
		args := typeArgs(x)
		for _, f := range x.Decl.Fields {
			fl.requestDestructors(resolve.Subst(f.Type, args), path)
		}
		for _, v := range x.Decl.Variants {
			for _, f := range v.Fields {
				fl.requestDestructors(resolve.Subst(f.Type, args), path)
			}
		}
	}
}

func typeArgs(n *resolve.Named) map[string]resolve.Type {
	m := map[string]resolve.Type{}
	for i, tp := range n.Decl.TypeParams {
		if i < len(n.Args) {
			m[tp.Name] = n.Args[i]
		}
	}
	return m
}

// endAccess closes an access value, calling its release function first when
// it has one.
func (fl *fnLower) endAccess(v *ir.Value) {
	if v == nil {
		return
	}
	if rel, ok := fl.relOf[v]; ok {
		// the obligation is the same on every exit path, so it is kept
		fl.emit(&ir.AccessEnd{Access: v, Release: rel.fn, Recv: rel.recv})
		return
	}
	fl.emit(&ir.AccessEnd{Access: v})
}

func (fl *fnLower) outFieldDrops(key any) {
	m := fl.l.info.OutFieldDrops[key]
	for sym, fields := range m {
		loc := fl.local(sym)
		n, _ := fl.ty(sym.Type).(*resolve.Named)
		for _, fname := range fields {
			if fname == "*" {
				fl.emitDrop(ir.Place{Local: loc}, loc.Type, sym.Pos)
				continue
			}
			if n == nil {
				continue
			}
			idx := fieldIndex(n, fname)
			_, ft := resolve.FieldType(n, fname)
			fl.emitDrop(ir.Place{Local: loc, Path: []ir.PathElem{{Kind: ir.PathField, Field: idx, Name: fname}}}, fl.ty(ft), sym.Pos)
		}
	}
}

func fieldIndex(n *resolve.Named, name string) int {
	for i, f := range n.Decl.Fields {
		if f.Name == name {
			return i
		}
	}
	return -1
}

var _ = fmt.Sprintf

// expansionDepth counts the instantiations of decl on the current path.
func expansionDepth(path map[string]bool, decl *resolve.TypeDecl) int {
	n := 0
	prefix := decl.Name + "["
	for k := range path {
		if k == decl.Name || strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// hasKernDestructor reports whether n's destructor is kern code to lower: a
// destructor of the main module, or of a non-opaque prelude type (the
// containers); the runtime destroys the opaque library objects.
func hasKernDestructor(n *resolve.Named, main string) bool {
	if n.Decl.Destructor == nil {
		return false
	}
	if n.Decl.Module == main {
		return true
	}
	return n.Decl.Module == "core" && !layout.Opaque(n)
}

// maxExpansion bounds the growing instantiations of one declaration along
// a destructor request chain (the bound marker derivation uses).
const maxExpansion = 8

// expanding reports whether requesting n's destructor from the function
// being lowered continues an expanding recursion: along the chain of
// functions that requested one another, more than maxExpansion destructors
// of the same declaration with a strictly smaller type (A[T] before
// A[Option[T]]). Finite nesting (Box[Box[u64]] destroying Box[u64]) shrinks
// along the chain and is never cut.
func (l *lowerer) expanding(n *resolve.Named) bool {
	depth := nestingDepth(n)
	grown := 0
	seen := map[string]bool{}
	for cur := l.cur; cur != "" && !seen[cur]; cur = l.parent[cur] {
		seen[cur] = true
		if t := l.dtorType[cur]; t != nil && t.Decl == n.Decl && nestingDepth(t) < depth {
			grown++
			if grown > maxExpansion {
				return true
			}
		}
	}
	return false
}

// nestingDepth is the maximum bracket depth of a type's name.
func nestingDepth(t resolve.Type) int {
	depth, max := 0, 0
	for _, c := range t.String() {
		switch c {
		case '[':
			depth++
			if depth > max {
				max = depth
			}
		case ']':
			depth--
		}
	}
	return max
}

// closure lowers a closure expression (spec §4): its body becomes a
// function of its own whose first parameters are the captured accesses
// (view or edit, as the resolver classified them) and whose remaining
// parameters are the closure's; the value is a MakeClosure over accesses
// opened here on the captured places, which the enclosing call ends.
func (fl *fnLower) closure(x *ast.ClosureExpr) operand {
	info := fl.l.info.Closures[x]
	if info == nil {
		fl.fail(x.Pos, "unresolved closure")
	}
	ft := fl.ty(info.Type).(*resolve.Func)
	fl.l.closures++
	name := fmt.Sprintf("%s$closure%d", fl.f.Name, fl.l.closures)
	cf := &ir.Func{Name: name, Pos: x.Pos, File: fl.file, Closure: true, Captures: len(info.Captures), Rets: append([]resolve.Type(nil), ft.Ret...)}
	var caps []*ir.Value
	for _, cp := range info.Captures {
		at := fl.captureType(cp)
		loc := &ir.Local{Name: cp.Sym.Name, Type: at, Conv: ast.ConvView}
		if cp.Edit {
			loc.Conv = ast.ConvEdit
		}
		cf.Params = append(cf.Params, loc)
		v := fl.newValue(at)
		fl.emit(&ir.AccessBegin{Dst: v, Edit: cp.Edit, Place: fl.symPlace(cp.Sym), Pos: cp.Ident.Pos})
		caps = append(caps, v)
	}
	for i, sym := range info.Params {
		fp := ft.Params[i]
		cf.Params = append(cf.Params, &ir.Local{Name: sym.Name, Type: fp.Type, Conv: fp.Conv})
	}
	fl2 := &fnLower{l: fl.l, f: cf, file: fl.file, subst: fl.subst, locals: map[*resolve.Symbol]*ir.Local{}, accs: map[*resolve.Symbol]*ir.Value{}, relOf: map[*ir.Value]releaseInfo{}, fd: fl.fd}
	entry := fl2.newBlock("entry")
	cf.Entry = entry
	fl2.cur = entry
	fl2.pushFrame()
	for i, cp := range info.Captures {
		loc := cf.Params[i]
		loc.ID = len(cf.Locals)
		cf.Locals = append(cf.Locals, loc)
		fl2.locals[cp.Sym] = loc
		v := fl2.newValue(loc.Type)
		fl2.emit(&ir.AccessBegin{Dst: v, Edit: cp.Edit, Place: ir.Place{Local: loc}, Pos: x.Pos})
		fl2.accs[cp.Sym] = v
	}
	for j, sym := range info.Params {
		loc := cf.Params[len(info.Captures)+j]
		loc.ID = len(cf.Locals)
		cf.Locals = append(cf.Locals, loc)
		fl2.locals[sym] = loc
		fl2.bindSym(sym)
		if loc.Conv == ast.ConvView || loc.Conv == ast.ConvEdit {
			v := fl2.newValue(loc.Type)
			fl2.emit(&ir.AccessBegin{Dst: v, Edit: loc.Conv == ast.ConvEdit, Place: ir.Place{Local: loc}, Pos: sym.Pos})
			fl2.accs[sym] = v
		}
	}
	fl2.block(x.Body)
	if fl2.cur != nil {
		fl2.exit(fl.l.info.Drops[x], 0)
		if len(cf.Rets) == 0 {
			fl2.terminate(&ir.Ret{})
		} else {
			fl2.terminate(&ir.Unreachable{})
		}
	}
	sortBlocks(cf)
	fl.l.done[name] = cf
	fl.l.parent[name] = fl.l.cur
	fl.l.mod.Add(cf)
	v := fl.newValue(ft)
	fl.emit(&ir.MakeClosure{Dst: v, Name: name, Captures: caps, Pos: x.Pos})
	fl.closureAccs = append(fl.closureAccs, caps...)
	return operand{val: v}
}

// captureType is the access type a captured symbol is passed as: an
// access to its value, a sequence access when the symbol is itself a
// sequence view or edit.
func (fl *fnLower) captureType(cp *resolve.Capture) *resolve.Access {
	t := fl.ty(cp.Sym.Type)
	a := &resolve.Access{Elem: t, Edit: cp.Edit}
	if sl, ok := t.(*resolve.Slice); ok {
		a.Seq = true
		a.Elem = sl.Elem
	}
	return a
}

// symPlace is the place a symbol denotes in this function: the access it
// holds (a binding or an access parameter) or its own slot.
func (fl *fnLower) symPlace(sym *resolve.Symbol) ir.Place {
	if acc, ok := fl.accs[sym]; ok {
		return ir.Place{Access: acc}
	}
	return ir.Place{Local: fl.local(sym)}
}
