package check

import (
	"sort"
	"fmt"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
)

// Check runs the ownership checker over every function body of the main
// module and of the prelude (the library containers are kern code) and
// returns diagnostics.
func Check(prog *resolve.Program) []diag.Diagnostic {
	return CheckModules(prog, nil)
}

// CheckModules checks the declarations of the modules keep selects (every
// module when keep is nil) of a resolved program: their diagnostics are
// exactly those Check reports in their files, since a module's checking
// reads only its own bodies and what resolution recorded about every
// module (the per-module cache, compiler/incremental).
func CheckModules(prog *resolve.Program, keep func(module string) bool) []diag.Diagnostic {
	c := &checker{prog: prog, info: prog.Info}
	// every module: the prelude, the std modules the program imports, the
	// imported user modules and the main module, in a fixed order
	// (only the selected ones: a cache miss checks one module, and must
	// not walk the whole program's files for it)
	kept := func(m *resolve.Module) bool { return m != nil && (keep == nil || keep(m.Name)) }
	var files []*ast.File
	if kept(prog.Core) {
		files = append(files, prog.Core.Files...)
	}
	var names []string
	for name, m := range prog.Modules {
		if m != prog.Core && m != prog.Main && kept(m) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		files = append(files, prog.Modules[name].Files...)
	}
	if prog.Main != prog.Core && kept(prog.Main) {
		files = append(files, prog.Main.Files...)
	}
	for _, f := range files {
		core := prog.IsStd(f.Module)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if fd := c.info.Funcs[d]; fd != nil && d.Body != nil {
					c.checkFunc(f.Name, fd, d.Body, false)
				}
				if d.Body == nil && d.Extern == "" && !core {
					c.errorf("SYNTAX", f.Name, d.Pos, "function %s has no body", d.Name)
				}
			case *ast.ImplDecl:
				for _, fn := range d.Funcs {
					if fd := c.info.Funcs[fn]; fd != nil && fn.Body != nil {
						c.checkFunc(f.Name, fd, fn.Body, false)
					} else if fn.Body == nil && !core {
						c.errorf("SYNTAX", f.Name, fn.Pos, "method %s has no body", fn.Name)
					}
				}
				if d.Destructor != nil {
					if self := c.info.Decls[d.Destructor]; self != nil {
						_ = self
					}
					c.checkDestructor(f.Name, d)
				}
			case *ast.StructDecl:
				for _, fl := range d.Fields {
					if _, ok := fl.Type.(*ast.AccessType); ok {
						c.errorf("ACC_ESCAPE_STORE", f.Name, fl.Pos, "field %s stores a second-class access", fl.Name)
					}
				}
				if d.View {
					c.errorf("ACC_ESCAPE_STORE", f.Name, d.Pos, "view struct is a reserved extension not enabled in this phase")
				}
			case *ast.UnionDecl:
				for _, v := range d.Variants {
					for _, fl := range v.Fields {
						if _, ok := fl.Type.(*ast.AccessType); ok {
							c.errorf("ACC_ESCAPE_STORE", f.Name, fl.Pos, "variant %s stores a second-class access", v.Name)
						}
					}
				}
			}
		}
	}
	diag.Sort(c.diags)
	return c.diags
}

type checker struct {
	prog  *resolve.Program
	info  *resolve.Info
	diags []diag.Diagnostic
}

// interiorEscalation reports whether an edit through a view place is the
// runtime-exclusive escalation of spec §6: inside unsafe, on a place whose
// root is an Interior type declared in the function's own module (rule 6.7).
func (f *fn) interiorEscalation(p *place) bool {
	if f.unsafe == 0 || p == nil || p.root == nil {
		return false
	}
	n, ok := valueType(p.root.Type).(*resolve.Named)
	if !ok || !resolve.IsInterior(n) {
		return false
	}
	return n.Decl.Module == f.fd.Module
}

func (c *checker) errorf(code diag.Code, file string, pos ast.Pos, format string, args ...any) {
	c.diags = append(c.diags, diag.New(code, file, diag.Span{Start: pos.Start, End: pos.End}, format, args...))
}

// fix attaches a machine-applicable edit to the diagnostic just reported.
func (c *checker) fix(ops ...diag.StructuredEditOp) {
	if n := len(c.diags); n > 0 {
		c.diags[n-1].Fix = &diag.StructuredEdit{Ops: ops}
	}
}

func span(p ast.Pos) diag.Span { return diag.Span{Start: p.Start, End: p.End} }

// fn is the per-function checking context.
type fn struct {
	c            *checker
	file         string
	fd           *resolve.FuncDecl
	now          int
	accs         []*access // accesses of the current statement
	encl         []*access // live accesses of enclosing bindings and scoped spawns
	scopes       []*bindScope
	unsafe       int
	hasUnsafe    bool
	accessParams []*resolve.Symbol // view/edit parameters (elision candidates)
	params       map[string]*resolve.Symbol
	loopDepth    int
	outParams    []*resolve.Symbol
	isDestructor bool
	bindRoots    map[*resolve.Symbol]*result
	loopExits    []loopExit
	guards       map[*resolve.Symbol]bool // with-bindings of a Mutex lock (Condvar.wait takes one)
	captureConv  map[*resolve.Symbol]ast.Conv // inside a closure body: how each captured symbol was captured
}

// bindScope is a lexical scope that owns bindings (with/match/for/block).
type bindScope struct {
	node     ast.Node
	bindings []*resolve.Symbol
	owned    []*resolve.Symbol // owned locals declared in this scope
	encl     int               // length of f.encl at scope entry
	scopeSym *resolve.Symbol   // the Scope binding for `with s := thread.scope()`
	isLoop   bool              // the scope of a loop (break/continue exit up to it)
}

// exitSyms lists the symbols whose scope ends when control leaves the scopes
// from the innermost down to index stop (inclusive), in destruction order:
// per scope, access bindings in reverse order, then initialized owned locals
// in reverse declaration order.
func (f *fn) exitSyms(st *flow, stop int) []*resolve.Symbol {
	var out []*resolve.Symbol
	for i := len(f.scopes) - 1; i >= stop; i-- {
		s := f.scopes[i]
		for j := len(s.bindings) - 1; j >= 0; j-- {
			out = append(out, s.bindings[j])
		}
		for j := len(s.owned) - 1; j >= 0; j-- {
			sym := s.owned[j]
			if vs, ok := st.vars[sym]; ok && vs.st == stInit {
				out = append(out, sym)
			}
		}
	}
	return out
}

// outFieldDrops lists the initialized fields of partially initialized out
// storage, for an error-path exit.
func (f *fn) outFieldDrops(st *flow) map[*resolve.Symbol][]string {
	var out map[*resolve.Symbol][]string
	for _, o := range f.outParams {
		vs, ok := st.vars[o]
		if !ok {
			continue
		}
		if vs.st == stInit {
			if out == nil {
				out = map[*resolve.Symbol][]string{}
			}
			out[o] = []string{"*"} // the whole value, by the ordinary rule
			continue
		}
		if vs.st != stPartial {
			continue
		}
		if out == nil {
			out = map[*resolve.Symbol][]string{}
		}
		// reverse initialization order is approximated by reverse declaration order
		if n, ok := o.Type.(*resolve.Named); ok {
			for i := len(n.Decl.Fields) - 1; i >= 0; i-- {
				if vs.fields[n.Decl.Fields[i].Name] {
					out[o] = append(out[o], n.Decl.Fields[i].Name)
				}
			}
		}
	}
	return out
}

// loopScopeIndex returns the index of the innermost loop scope, or 0.
func (f *fn) loopScopeIndex() int {
	for i := len(f.scopes) - 1; i >= 0; i-- {
		if f.scopes[i].isLoop {
			return i
		}
	}
	return 0
}

func (f *fn) errorf(code diag.Code, pos ast.Pos, format string, args ...any) {
	f.c.errorf(code, f.file, pos, format, args...)
}

func (f *fn) fix(ops ...diag.StructuredEditOp) { f.c.fix(ops...) }

func (f *fn) tick() int { f.now++; return f.now }

func (c *checker) checkFunc(file string, fd *resolve.FuncDecl, body *ast.Block, isDtor bool) {
	f := &fn{c: c, file: file, fd: fd, params: map[string]*resolve.Symbol{}, isDestructor: isDtor}
	st := newFlow()
	// Parameters are symbols recorded by the resolver on the FuncDecl (self) and
	// via Idents; we recover them by scanning Idents for SymParam of this
	// function. Simpler: the resolver stores params in Decls keyed by the Param.
	for _, p := range fd.Params {
		sym := c.paramSymbol(fd, p)
		if sym == nil {
			continue
		}
		f.params[p.Name] = sym
		switch p.Conv {
		case ast.ConvView, ast.ConvEdit:
			f.accessParams = append(f.accessParams, sym)
		case ast.ConvOut:
			st.vars[sym] = &varState{st: stUninit, fields: map[string]bool{}}
			f.outParams = append(f.outParams, sym)
		default:
			st.vars[sym] = &varState{st: stInit}
		}
	}
	outer := f.pushScope(body)
	for _, p := range fd.Params {
		if sym := f.params[p.Name]; sym != nil && (p.Conv == ast.ConvOwn || p.Conv == ast.ConvValue) {
			outer.owned = append(outer.owned, sym)
		}
	}
	end := f.block(st, body)
	if !end.dead {
		f.normalReturn(end, body.Pos)
		f.c.info.Drops[fd.AST] = f.exitSyms(end, 0)
	}
	f.popScope()
	if f.hasUnsafe && fd.Contract == nil {
		f.errorf("UNSAFE_MISSING_CONTRACT", fd.Pos, "function %s contains unsafe but has no contract block", fd.Name)
		f.fix(diag.InsertAt(f.file, fd.Pos.Start, "contract { requires \"TODO: state what the unsafe code relies on\" }\n"))
	}
	if len(fd.Ret) > 1 && !f.hasUnsafe && !fd.Intrinsic {
		f.errorf("ACC_MULTI_ACCESS_SAFE", fd.Pos, "function %s returns several accesses but is not implemented inside unsafe", fd.Name)
	}
}

func (c *checker) checkDestructor(file string, d *ast.ImplDecl) {
	fd := &resolve.FuncDecl{Name: "destructor", Pos: d.Destructor.Pos, Type: &resolve.Func{}}
	self := c.info.Decls[d.Destructor]
	f := &fn{c: c, file: file, fd: fd, params: map[string]*resolve.Symbol{}, isDestructor: true}
	if self != nil {
		f.params["self"] = self
		f.accessParams = append(f.accessParams, self)
	}
	st := newFlow()
	f.pushScope(d.Destructor.Body)
	f.block(st, d.Destructor.Body)
	f.popScope()
	if f.hasUnsafe {
		f.errorf("UNSAFE_MISSING_CONTRACT", d.Destructor.Pos, "destructor contains unsafe but destructors cannot carry a contract block; move the unsafe code to a contracted function")
	}
}

// paramSymbol finds the resolver's symbol for a parameter.
func (c *checker) paramSymbol(fd *resolve.FuncDecl, p *resolve.ParamDecl) *resolve.Symbol {
	if sym := c.info.Decls[p]; sym != nil {
		return sym
	}
	return nil
}

func (f *fn) pushScope(n ast.Node) *bindScope {
	s := &bindScope{node: n, encl: len(f.encl)}
	f.scopes = append(f.scopes, s)
	return s
}

func (f *fn) popScope() {
	s := f.scopes[len(f.scopes)-1]
	f.scopes = f.scopes[:len(f.scopes)-1]
	f.encl = f.encl[:s.encl]
}

func (f *fn) curScope() *bindScope { return f.scopes[len(f.scopes)-1] }

// normalReturn checks obligations at a normal (non-error) return.
func (f *fn) normalReturn(st *flow, pos ast.Pos) {
	for _, o := range f.outParams {
		if v := st.vars[o]; v == nil || v.st != stInit {
			f.errorf("INIT_OUT_NOT_SET", pos, "out parameter %s is not fully initialized on this return path", o.Name)
		}
	}
}

var _ = fmt.Sprintf
