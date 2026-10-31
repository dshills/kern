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
	return EmitWith(mod, Options{Triple: triple})
}

// Options selects the emitted program's environment (PLAN.md §0).
type Options struct {
	Triple string
	// Freestanding emits `kern_main` for the freestanding startup code
	// instead of `main`.
	Freestanding bool
	// StackCheck inserts the freestanding stack-limit check in every
	// prologue (spec §6); off only through the unsafe build option.
	StackCheck bool
	// TSan marks every function for ThreadSanitizer instrumentation.
	TSan bool
	// ElideChecks drops every bounds, overflow and conversion check: a
	// measurement-only option (PLAN.md 4.4, the intrinsic-overhead
	// separation), never a build configuration of the language.
	ElideChecks bool
	// Sources enables DWARF emission: file name to bytes, for line and
	// column numbers (PLAN.md Phase 5 debugger).
	Sources map[string][]byte
}

// EmitWith renders a module for the given options.
func EmitWith(mod *ir.Module, opts Options) (string, []error) {
	e := &emitter{mod: mod, opts: opts, types: map[string]bool{}, glue: map[string]bool{}, runtime: map[string]bool{}, files: map[string]string{}, locs: map[string]string{}, fnLocIdx: map[string]int64{}}
	if opts.Sources != nil {
		e.dbg = newDebugInfo(opts.Sources)
	}
	// a trap never returns and its block is cold, which keeps the checks
	// out of the inliner's cost and the hot path's layout
	e.runtime["kern_trap"] = true
	e.decls.WriteString("declare void @kern_trap(i32, i64, i64, ptr) noreturn cold\n")
	if opts.StackCheck {
		e.decls.WriteString("@kern_stack_limit = external global ptr\n")
		e.declare("llvm.stacksave.p0", "ptr ()")
	}
	triple := opts.Triple
	for _, g := range mod.Globals {
		if g.ReadOnly {
			fmt.Fprintf(&e.decls, "%s = constant %s %s\n", globalSym(g), e.mty(g.Type), e.globalInit(g))
			continue
		}
		fmt.Fprintf(&e.decls, "%s = thread_local global %s %s\n", globalSym(g), e.mty(g.Type), e.globalInit(g))
	}
	for _, f := range mod.Funcs {
		if f.Extern != "" && len(f.Blocks) == 0 {
			e.externDecl(f)
			continue
		}
		if f.Intrinsic || f.Inlined {
			continue
		}
		e.function(f)
	}
	if len(mod.Tests) > 0 {
		e.testMain(mod)
	} else if mod.Entry != "" {
		entry := mod.Func(mod.Entry)
		ret := e.retType(entry)
		entrySym := "@main"
		if opts.Freestanding {
			entrySym = "@kern_main" // the startup code calls it and halts with its result
		}
		// a hosted program's main receives argc and argv and hands them to
		// the runtime for std/io's args(); a freestanding image has none
		// the program's file, which a trap raised by the runtime itself
		// (a stack check, a mutex misuse) names in its record: the file
		// declaring main
		params, setArgs := "", e.fnLocTable()
		if entry.File != "" {
			setArgs += fmt.Sprintf("  call void @kern_set_file(ptr %s)\n", e.fileSym(entry.File))
			e.declare("kern_set_file", "void (ptr)")
		}
		if !opts.Freestanding {
			params = "i32 %argc, ptr %argv"
			setArgs += "  call void @kern_set_args(i32 %argc, ptr %argv)\n"
			e.declare("kern_set_args", "void (i32, ptr)")
		}
		if ret == "void" {
			fmt.Fprintf(&e.sb, "define i32 %s(%s)%s {\nentry:\n%s  call void @kern_start()\n  call %s %s()\n  ret i32 0\n}\n", entrySym, params, e.entryMarker(mod.Entry), setArgs, retOrVoid(ret), fnSym(mod.Entry))
		} else {
			fmt.Fprintf(&e.sb, "define i32 %s(%s)%s {\nentry:\n%s  call void @kern_start()\n  %%r = call %s %s()\n  %%s = %s %s %%r to i32\n  ret i32 %%s\n}\n", entrySym, params, e.entryMarker(mod.Entry), setArgs, ret, fnSym(mod.Entry), widenOp(ret), ret)
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
	out.WriteString(e.tail.String())
	if e.dbg != nil {
		out.WriteString(e.dbg.metadata())
	}
	if attrs := e.fnAttrs(); attrs != "" {
		fmt.Fprintf(&out, "attributes #0 = {%s }\n", attrs)
	}
	return out.String(), e.errs
}

// fnAttrs is the attribute group every function carries. Hosted builds probe
// every frame page by page so a large frame cannot skip the guard page
// (PLAN.md 2.2); ThreadSanitizer builds instrument every function. A
// freestanding build without ThreadSanitizer needs neither, and LLVM rejects
// an empty attribute group, so the group is then omitted altogether.
func (e *emitter) fnAttrs() string {
	attrs := ""
	if !e.opts.StackCheck {
		attrs += " \"probe-stack\"=\"inline-asm\"" // hosted: the guard page must not be skipped
	}
	if e.opts.TSan {
		attrs += " sanitize_thread"
	}
	return attrs
}

// attrRef is the reference to the attribute group on a function definition,
// empty when fnAttrs emits no group.
func (e *emitter) attrRef() string {
	if e.fnAttrs() == "" {
		return ""
	}
	return " #0"
}

// testMain emits the entry of a `kern test` executable
// (specs/test-command/SPEC.md R7): argv[1] is the index of the test to
// run in mod.Tests, a decimal integer the whole argument spells (strtol
// must consume it all and not overflow); the program's arguments are its
// name alone, its file (what a runtime-raised trap names) is the test's,
// and returning from the test exits 0. A missing, malformed or unknown
// index exits 2 without running anything.
func (e *emitter) testMain(mod *ir.Module) {
	e.declare("strtol", "i64 (ptr, ptr, i32)")
	e.declare("kern_set_file", "void (ptr)")
	e.declare("kern_set_args", "void (i32, ptr)")
	e.declare("kern_start", "void ()")
	var b strings.Builder
	b.WriteString("define i32 @main(i32 %argc, ptr %argv) {\nentry:\n")
	b.WriteString(e.fnLocTable())
	b.WriteString("  %few = icmp slt i32 %argc, 2\n  br i1 %few, label %usage, label %pick\n")
	b.WriteString("usage:\n  ret i32 2\n")
	b.WriteString("pick:\n  %endp = alloca ptr\n  %a1p = getelementptr ptr, ptr %argv, i64 1\n  %a1 = load ptr, ptr %a1p\n")
	b.WriteString("  %c0 = load i8, ptr %a1\n  %digit0 = sub i8 %c0, 48\n  %isdigit = icmp ult i8 %digit0, 10\n  br i1 %isdigit, label %parse, label %usage\n")
	b.WriteString("parse:\n  %n = call i64 @strtol(ptr %a1, ptr %endp, i32 10)\n  %end = load ptr, ptr %endp\n  %c = load i8, ptr %end\n  %whole = icmp eq i8 %c, 0\n  br i1 %whole, label %dispatch, label %usage\n")
	b.WriteString("dispatch:\n")
	var cases []string
	for i := range mod.Tests {
		cases = append(cases, fmt.Sprintf("i64 %d, label %%t%d", i, i))
	}
	fmt.Fprintf(&b, "  switch i64 %%n, label %%usage [ %s ]\n", strings.Join(cases, " "))
	for i, name := range mod.Tests {
		f := mod.Func(name)
		fmt.Fprintf(&b, "t%d:\n", i)
		if f != nil && f.File != "" {
			fmt.Fprintf(&b, "  call void @kern_set_file(ptr %s)\n", e.fileSym(f.File))
		}
		// the file's setup before the test, its teardown after the test
		// returns (specs/test-runs R2); noinline: a function inlined into
		// the dispatcher would lose its own location marker
		// (specs/native-trap-loc)
		fmt.Fprintf(&b, "  call void @kern_set_args(i32 1, ptr %%argv)\n  call void @kern_start()\n")
		if i < len(mod.TestSetup) && mod.TestSetup[i] != "" {
			fmt.Fprintf(&b, "  call void %s() noinline\n", fnSym(mod.TestSetup[i]))
		}
		fmt.Fprintf(&b, "  call void %s() noinline\n", fnSym(name))
		if i < len(mod.TestTeardown) && mod.TestTeardown[i] != "" {
			fmt.Fprintf(&b, "  call void %s() noinline\n", fnSym(mod.TestTeardown[i]))
		}
		b.WriteString("  ret i32 0\n")
	}
	b.WriteString("}\n")
	e.sb.WriteString(b.String())
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
	e        *emitter
	f        *ir.Func
	body     strings.Builder
	allocas  strings.Builder
	tmp      int
	vals     map[*ir.Value]string // LLVM register names
	accs     map[*ir.Value]bool   // values defined by access_begin (they hold addresses)
	addrs    map[*ir.Value]string // large aggregate values kept in memory: their private copy
	pstores  strings.Builder      // parameter stores into their slots (after the stack check)
	strs     map[*ir.Value][]byte // string literal values, for inline assembly templates
	tags     map[*ir.Value]int    // payload-free union literals (memory orderings): their tag
	packed   map[*ir.Value]bool   // accesses opened inside a packed struct (unaligned places)
	curLabel string               // the basic block being written (a predecessor of the trap block)
	traps    []trapEdge           // the checks that branch to the function's trap block
	dbgLoc   string               // the current instruction's !DILocation, "" without debug info
	align    int                  // alignment override for the next load/store (1 inside packed structs)
	labels   map[*ir.Block]string
	cur      *ir.Block
}

// trapEdge is one check's branch into the function's shared trap block:
// the block it comes from, and the kind and span its phi entries carry.
type trapEdge struct {
	from  string
	kind  int
	start string
	end   string
	file  string // the effective file of the check's block ("" for none); rendered at the landing block
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
	line := fmt.Sprintf(format, args...)
	// instructions of a function with debug info carry their location;
	// labels and terminators of trap blocks are left alone
	if fe.dbgLoc != "" && !strings.HasSuffix(line, ":") && !strings.HasPrefix(line, "unreachable") && !strings.HasPrefix(line, "%dead") {
		line += ", !dbg " + fe.dbgLoc
	}
	fmt.Fprintf(&fe.body, "  %s\n", line)
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
// envType is the LLVM type of a closure function's environment: a struct
// of its captured accesses' parameter types.
func (e *emitter) envType(f *ir.Func) string {
	var parts []string
	for i := 0; i < f.Captures && i < len(f.Params); i++ {
		parts = append(parts, e.paramType(f.Params[i]))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

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

// globalSym is the symbol of a global: a thread_local or an aggregate constant.
func globalSym(g *ir.Global) string { return "@" + quote("g."+g.Name) }

// globalInit renders a global's constant initializer.
func (e *emitter) globalInit(g *ir.Global) string { return e.constInit(g.Type, g.Init) }

// constInit renders a folded initializer (ir.Global) as an LLVM constant of
// the memory type of t: a scalar, or an array or struct aggregate whose
// elements are rendered recursively, each with its own type; nil is the
// zero value at any depth.
func (e *emitter) constInit(t resolve.Type, init any) string {
	switch v := init.(type) {
	case int64:
		return fmt.Sprintf("%d", v)
	case float64:
		return fconst(t, v)
	case bool:
		if v {
			return "1"
		}
		return "0"
	case []any:
		switch x := t.(type) {
		case *resolve.Array:
			parts := make([]string, len(v))
			for i, el := range v {
				parts[i] = e.mty(x.Elem) + " " + e.constInit(x.Elem, el)
			}
			return "[" + strings.Join(parts, ", ") + "]"
		case *resolve.Named:
			if len(x.Decl.Fields) == 0 {
				return "zeroinitializer" // an empty struct is laid out as one byte
			}
			args := typeArgs(x)
			parts := make([]string, len(v))
			for i, f := range x.Decl.Fields {
				ft := resolve.Subst(f.Type, args)
				parts[i] = e.mty(ft) + " " + e.constInit(ft, v[i])
			}
			if x.Decl.Packed {
				return "<{ " + strings.Join(parts, ", ") + " }>"
			}
			return "{ " + strings.Join(parts, ", ") + " }"
		}
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
	fe := &fnEmit{e: e, f: f, vals: map[*ir.Value]string{}, accs: map[*ir.Value]bool{}, addrs: map[*ir.Value]string{}, strs: map[*ir.Value][]byte{}, tags: map[*ir.Value]int{}, packed: map[*ir.Value]bool{}, labels: map[*ir.Block]string{}}
	for _, b := range f.Blocks {
		fe.labels[b] = fmt.Sprintf("bb%d", b.ID)
	}
	var ps []string
	if f.Closure {
		ps = append(ps, "ptr %env") // the captures, stored by MakeClosure
	}
	for i, p := range f.Params {
		if f.Closure && i < f.Captures {
			continue
		}
		ps = append(ps, e.paramDecls(f, p, i)...)
	}
	if f.Library {
		// the span and file of the user-level call that entered the
		// library: every trap inside reports them
		ps = append(ps, "i64 %span.s", "i64 %span.e", "ptr %span.f")
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
		if f.Closure && i < f.Captures {
			// a captured access arrives through the environment
			fmt.Fprintf(&fe.pstores, "  %%p%d.env = getelementptr %s, ptr %%env, i32 0, i32 %d\n  %%p%d = load %s, ptr %%p%d.env\n  store %s %%p%d, ptr %s\n", i, e.envType(f), i, i, pt, i, pt, i, fe.local(p))
			continue
		}
		if p.Conv == ast.ConvOut || p.Conv == ast.ConvView || p.Conv == ast.ConvEdit {
			if pt == "{ ptr, i64 }" {
				// a sequence access arrives as two scalars
				fmt.Fprintf(&fe.pstores, "  %%p%d.a = insertvalue { ptr, i64 } undef, ptr %%p%d.p, 0\n  %%p%d = insertvalue { ptr, i64 } %%p%d.a, i64 %%p%d.n, 1\n", i, i, i, i, i)
			}
			fmt.Fprintf(&fe.pstores, "  store %s %%p%d, ptr %s\n", pt, i, fe.local(p))
			continue
		}
		if pt == "i1" {
			fmt.Fprintf(&fe.pstores, "  %%pz%d = zext i1 %%p%d to i8\n  store i8 %%pz%d, ptr %s\n", i, i, i, fe.local(p))
			continue
		}
		fmt.Fprintf(&fe.pstores, "  store %s %%p%d, ptr %s\n", pt, i, fe.local(p))
	}
	for _, b := range f.Blocks {
		fe.cur = b
		fmt.Fprintf(&fe.body, "%s:\n", fe.labels[b])
		fe.curLabel = fe.labels[b]
		for _, in := range b.Instrs {
			if e.dbg != nil {
				fe.dbgLoc = e.dbg.location(f, b, instrPos(in))
			}
			fe.instr(in)
		}
		if e.dbg != nil {
			fe.dbgLoc = e.dbg.location(f, nil, f.Pos)
		}
		fe.term(b.Term)
	}
	if len(fe.traps) > 0 {
		// one trap landing block per function: every check is a compare
		// and a branch here, and the kind and span it reports arrive by phi,
		// so the checks cost LLVM's inliner one call, not one per check
		// the file too, by phi, only when some check sits in a block the
		// inliner copied from another file; otherwise the function's own,
		// as a constant. The edges' operands are rendered here, after the
		// function's, so that a one-file program interns its file constant
		// at the same point as before and its output is byte-identical
		// (specs/cross-file-inlining/SPEC.md R11).
		file := fe.funcFile()
		var kinds, starts, ends, files []string
		differ := false
		for _, t := range fe.traps {
			op := file
			if !fe.f.Library {
				op = fe.fileOperand(t.file)
			}
			kinds = append(kinds, fmt.Sprintf("[ %d, %%%s ]", t.kind, t.from))
			starts = append(starts, fmt.Sprintf("[ %s, %%%s ]", t.start, t.from))
			ends = append(ends, fmt.Sprintf("[ %s, %%%s ]", t.end, t.from))
			files = append(files, fmt.Sprintf("[ %s, %%%s ]", op, t.from))
			differ = differ || op != file
		}
		fmt.Fprintf(&fe.body, "trap:\n")
		fe.emit("%%trap.kind = phi i32 %s", strings.Join(kinds, ", "))
		fe.emit("%%trap.start = phi i64 %s", strings.Join(starts, ", "))
		fe.emit("%%trap.end = phi i64 %s", strings.Join(ends, ", "))
		if differ {
			fe.emit("%%trap.file = phi ptr %s", strings.Join(files, ", "))
			file = "%trap.file"
		}
		fe.emit("call void @kern_trap(i32 %%trap.kind, i64 %%trap.start, i64 %%trap.end, ptr %s)", file)
		fe.emit("unreachable")
	}
	// the entry block: allocas (a stack-pointer adjustment only), then in
	// freestanding builds the stack-limit check before anything is written
	// into the frame (the limit sits a guard margin above the real bottom,
	// which covers the callee-saved spills LLVM makes at the frame's top;
	// no stack probes are emitted in that mode), then the parameter stores
	entry := fe.allocas.String()
	if e.opts.StackCheck {
		entry += fmt.Sprintf("  %%sp = call ptr @llvm.stacksave.p0()\n  %%lim = load ptr, ptr @kern_stack_limit\n  %%ovf = icmp ult ptr %%sp, %%lim\n  br i1 %%ovf, label %%stackovf, label %%frame\nstackovf:\n  call void @kern_trap(i32 %d, %s, ptr %s)\n  unreachable\nframe:\n", trapKind("stack"), fe.span(f.Pos), fe.funcFile())
	}
	entry += fe.pstores.String() + fmt.Sprintf("  br label %%bb%d\n", f.Entry.ID)
	dbgSuffix := ""
	if e.dbg != nil {
		dbgSuffix = " !dbg " + e.dbg.subprogram(f)
	}
	linkage, attrs := "", ""
	if f.Library {
		linkage = "internal " // a library instantiation is private to the program: inlined and discarded
		if ir.ColdLibrary[baseName(f.Name)] {
			attrs = " noinline cold" // a growth or allocation path: kept out of the hot operations that call it
		}
	} else if (e.mod.Entry != "" || len(e.mod.Tests) > 0) && f.Extern == "" {
		// a program's own functions are reached only from the module (the
		// entry through its wrapper, the rest by call or function value), so
		// they are private too, except one exported under a C name: LLVM then
		// inlines a function called from one place whatever its size and
		// discards the copy (docs/reports/gap-diagnosis.md)
		linkage = "internal "
	}
	fmt.Fprintf(&e.sb, "define %s%s %s(%s)%s%s%s%s {\nentry:\n%s%s}\n\n", linkage, e.retType(f), e.symOf(f), strings.Join(ps, ", "), attrs, e.attrRef(), fe.marker(), dbgSuffix, entry, fe.body.String())
}

// locMagic opens a function's location marker (specs/native-trap-loc):
// "KERNLOC!" read as a little-endian word.
const locMagic = 0x21434f4c4e52454b

// marker places a hosted function in the kern text section with a prefix
// the runtime's fault handler reads to name the function a native `stack`
// trap, or a fault in safe code, happened in: the magic word, then the
// index of the function's location (its declaration's span and file) in
// the table the entry hands the runtime, or -1 for a library function,
// whose location is its caller's. A table of function addresses would be
// a use of each function and keep LLVM from discarding a function it
// inlined into its one caller, and a pointer in the prefix is a text
// relocation, which macOS refuses; an index is neither. A freestanding
// build checks the stack limit in every prologue instead, with the
// function's location, so it carries no marker.
func (fe *fnEmit) marker() string {
	e := fe.e
	if e.opts.Freestanding {
		return ""
	}
	idx := int64(-1)
	if !fe.f.Library {
		idx = int64(len(e.fnLocs))
		e.fnLocIdx[fe.f.Name] = idx
		e.fnLocs = append(e.fnLocs, fmt.Sprintf("{ i64, i64, ptr } { i64 %d, i64 %d, ptr %s }", fe.f.Pos.Start, fe.f.Pos.End, fe.fileOperand(fe.f.File)))
	}
	return e.markerFor(idx)
}

func (e *emitter) markerFor(idx int64) string {
	return fmt.Sprintf(" section \"%s\" prefix { i64, i64 } { i64 %d, i64 %d }", kernTextSection(e.opts.Triple), uint64(locMagic), idx)
}

// entryMarker marks the hosted entry wrapper with the program's main
// function's location: LLVM inlines main into it, and a fault there is
// main's.
func (e *emitter) entryMarker(entry string) string {
	if e.opts.Freestanding {
		return ""
	}
	idx, ok := e.fnLocIdx[entry]
	if !ok {
		idx = -1
	}
	return e.markerFor(idx)
}

// fnLocTable emits the table of function locations the markers index and
// the call that hands it to the runtime, for the entry wrapper.
func (e *emitter) fnLocTable() string {
	if e.opts.Freestanding {
		return ""
	}
	e.declare("kern_set_fn_locs", "void (ptr, i64)")
	fmt.Fprintf(&e.tail, "@kern.fn_locs = private constant [%d x { i64, i64, ptr }] [%s]\n", len(e.fnLocs), strings.Join(e.fnLocs, ", "))
	return fmt.Sprintf("  call void @kern_set_fn_locs(ptr @kern.fn_locs, i64 %d)\n", len(e.fnLocs))
}

// kernTextSection is the section every hosted kern function is placed in,
// whose bounds the runtime reads from the linker's symbols for it.
func kernTextSection(triple string) string {
	if strings.Contains(triple, "apple") || strings.Contains(triple, "darwin") || strings.Contains(triple, "macos") {
		return "__TEXT,__kern_text,regular,pure_instructions"
	}
	return "kern_text"
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
	align  int  // 1 inside a packed struct (unaligned access), else 0
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

// step derives a sub-place; the alignment override of a packed access is
// kept through every path element.
func (fe *fnEmit) step(cur pl, el ir.PathElem) pl {
	next := fe.step1(cur, el)
	if cur.align != 0 && next.align == 0 {
		next.align = cur.align
	}
	return next
}

func (fe *fnEmit) step1(cur pl, el ir.PathElem) pl {
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
		al := cur.align
		if n.Decl.Packed {
			al = 1
		}
		return pl{ptr: r, ty: ft, align: al}
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
		fe.emit("%s = load i8, ptr %s%s", r, ptr, fe.alignSuffix())
		fe.emit("%s = trunc i8 %s to i1", dst, r)
		return
	}
	fe.emit("%s = load %s, ptr %s%s", dst, fe.e.lty(t), ptr, fe.alignSuffix())
}

// alignSuffix renders the alignment override of the current access.
func (fe *fnEmit) alignSuffix() string {
	if fe.align > 0 {
		return fmt.Sprintf(", align %d", fe.align)
	}
	return ""
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
	fe.emit("store %s %s, ptr %s%s", fe.e.lty(t), val, ptr, fe.alignSuffix())
}

// ---- instructions ----

// check emits `br i1 cond, trap, cont`: the trap block is the function's
// shared landing block, which reports kind and the span by phi.
func (fe *fnEmit) check(cond string, kind string, pos ast.Pos) {
	fe.tmp++
	c := fmt.Sprintf("ok%d", fe.tmp)
	fe.emit("br i1 %s, label %%trap, label %%%s", cond, c)
	start, end := fe.spanParts(pos)
	fe.traps = append(fe.traps, trapEdge{from: fe.curLabel, kind: trapKind(kind), start: start, end: end, file: fe.blockFile()})
	fmt.Fprintf(&fe.body, "%s:\n", c)
	fe.curLabel = c
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
		fe.align = p.align
		fe.loadFrom(fe.v(i.Dst), i.Dst.Type, p.ptr)
		fe.align = 0
	case *ir.Store:
		p := fe.place(i.Place)
		fe.align = p.align
		fe.storeValue(i.Src, p.ptr)
		fe.align = 0
	case *ir.Move:
		p := fe.place(i.Src)
		if isLarge(i.Dst.Type) {
			fe.loadLarge(i.Dst, p.ptr)
			return
		}
		fe.align = p.align
		fe.loadFrom(fe.v(i.Dst), i.Dst.Type, p.ptr)
		fe.align = 0
	case *ir.Drop:
		if g := e.glueFor(i.Type); g != "null" {
			p := fe.place(i.Place)
			fe.emit("call void %s(ptr %s)", g, p.ptr)
		}
	case *ir.AccessBegin:
		fe.accs[i.Dst] = true
		p := fe.place(i.Place)
		if p.align == 1 {
			fe.packed[i.Dst] = true
		}
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
		} else if intr, ok := bitCountIntrinsics[i.Op]; ok {
			// ctz/clz of zero is the width (is_zero_poison false); the result is u32
			bits := layout.Of(i.X.Type).Size * 8
			r := fe.next("t")
			if i.Op == "popcount" {
				fe.e.declare(intr+"."+t, t+" ("+t+")")
				fe.emit("%s = call %s @%s.%s(%s %s)", r, t, intr, t, t, fe.v(i.X))
			} else {
				fe.e.declare(intr+"."+t, t+" ("+t+", i1)")
				fe.emit("%s = call %s @%s.%s(%s %s, i1 false)", r, t, intr, t, t, fe.v(i.X))
			}
			switch {
			case bits < 32:
				fe.emit("%s = zext %s %s to i32", fe.v(i.Dst), t, r)
			case bits > 32:
				fe.emit("%s = trunc %s %s to i32", fe.v(i.Dst), t, r)
			default:
				fe.vals[i.Dst] = r
			}
		} else if resolve.IsFloat(i.X.Type) {
			fe.emit("%s = fneg %s %s", fe.v(i.Dst), t, fe.v(i.X))
		} else {
			fe.emit("%s = sub %s 0, %s", fe.v(i.Dst), t, fe.v(i.X))
		}
	case *ir.Conv:
		fe.conv(i)
	case *ir.CheckOverflow:
		if e.opts.ElideChecks {
			return
		}
		fe.checkOverflow(i)
	case *ir.CheckBounds:
		if e.opts.ElideChecks || i.Prov.Label == "proven" {
			// a proven check (spec §7 invariant 3): the lowering recorded
			// the fact that makes it redundant; the interpreter still
			// executes it, which the differential harness relies on
			return
		}
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
		if e.opts.ElideChecks {
			return
		}
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
	case *ir.MakeClosure:
		// the environment is a frame slot holding the captured accesses;
		// the value pairs the closure function with it
		cf := e.mod.Func(i.Name)
		if cf == nil {
			fe.fail("unknown closure %s", i.Name)
			return
		}
		envTy := e.envType(cf)
		env := fe.next("env")
		fmt.Fprintf(&fe.allocas, "  %s = alloca %s\n", env, envTy)
		for k, c := range i.Captures {
			g := fe.next("t")
			fe.emit("%s = getelementptr %s, ptr %s, i32 0, i32 %d", g, envTy, env, k)
			fe.emit("store %s %s, ptr %s", e.paramType(cf.Params[k]), fe.v(c), g)
		}
		a := fe.next("t")
		fe.emit("%s = insertvalue { ptr, ptr } undef, ptr %s, 0", a, fnSym(i.Name))
		fe.emit("%s = insertvalue { ptr, ptr } %s, ptr %s, 1", fe.v(i.Dst), a, env)
	case *ir.FuncRef:
		if f := e.mod.Func(i.Name); f != nil && f.Extern != "" {
			fe.vals[i.Dst] = "@" + bareName(i.Name)
		} else if f != nil && f.Library {
			fe.vals[i.Dst] = e.libraryThunk(f)
		} else {
			fe.vals[i.Dst] = fnSym(i.Name)
		}
	case *ir.Trap:
		fe.emit("call void @kern_trap(i32 %d, %s)", trapKind(i.Kind), fe.spanFile(i.Pos))
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
		fe.strs[c.Dst] = c.Bytes
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
	if len(i.Fields) == 0 {
		fe.tags[i.Dst] = i.Tag // a constant an atomic operation may take as its ordering
	}
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

// bitCountIntrinsics maps the bit-count unary operations to LLVM intrinsics.
var bitCountIntrinsics = map[string]string{"ctz": "llvm.cttz", "clz": "llvm.ctlz", "popcount": "llvm.ctpop"}

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
			// a set bit of the representation shifted out traps (spec §4.1),
			// for signed values too: the bits come back logically, so
			// -1 << 1 traps where an arithmetic shift back would pass it
			back := fe.next("t")
			fe.emit("%s = lshr %s %s, %s", back, lt, s, c)
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

// libraryThunk emits, once, a wrapper with the plain signature of a library
// function (no span parameters) so that it can be taken as a function value;
// traps inside report an empty span and the program's file.
func (e *emitter) libraryThunk(f *ir.Func) string {
	name := f.Name + ".fnptr"
	sym := fnSym(name)
	if e.glue[name] {
		return sym
	}
	e.glue[name] = true
	var ps, args []string
	for i, p := range f.Params {
		decls := e.paramDecls(f, p, i)
		ps = append(ps, decls...)
		args = append(args, decls...)
	}
	args = append(args, "i64 0", "i64 0", "ptr null")
	ret := e.retType(f)
	var b strings.Builder
	fmt.Fprintf(&b, "define internal %s %s(%s) {\nentry:\n", ret, sym, strings.Join(ps, ", "))
	if ret == "void" {
		fmt.Fprintf(&b, "  call void %s(%s)\n  ret void\n}\n", fnSym(f.Name), strings.Join(args, ", "))
	} else {
		fmt.Fprintf(&b, "  %%r = call %s %s(%s)\n  ret %s %%r\n}\n", ret, fnSym(f.Name), strings.Join(args, ", "), ret)
	}
	e.glueBody.WriteString(b.String())
	return sym
}


// baseName strips the specialization suffix: "Vec.grow[T=u64]" -> "Vec.grow".
func baseName(name string) string {
	if i := strings.Index(name, "["); i >= 0 {
		return name[:i]
	}
	return name
}
