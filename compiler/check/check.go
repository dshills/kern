package check

import (
	"fmt"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
)

// Check runs the ownership checker over every function body of the main
// module and returns diagnostics.
func Check(prog *resolve.Program) []diag.Diagnostic {
	c := &checker{prog: prog, info: prog.Info}
	for _, f := range prog.Main.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if fd := c.info.Funcs[d]; fd != nil && d.Body != nil {
					c.checkFunc(f.Name, fd, d.Body, false)
				}
				if d.Body == nil && d.Extern == "" {
					c.errorf("SYNTAX", f.Name, d.Pos, "function %s has no body", d.Name)
				}
			case *ast.ImplDecl:
				for _, fn := range d.Funcs {
					if fd := c.info.Funcs[fn]; fd != nil && fn.Body != nil {
						c.checkFunc(f.Name, fd, fn.Body, false)
					} else if fn.Body == nil {
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

func (c *checker) errorf(code diag.Code, file string, pos ast.Pos, format string, args ...any) {
	c.diags = append(c.diags, diag.New(code, file, diag.Span{Start: pos.Start, End: pos.End}, format, args...))
}

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
}

// bindScope is a lexical scope that owns bindings (with/match/for/block).
type bindScope struct {
	node     ast.Node
	bindings []*resolve.Symbol
	owned    []*resolve.Symbol // owned locals declared in this scope
	encl     int               // length of f.encl at scope entry
	scopeSym *resolve.Symbol   // the Scope binding for `with s := thread.scope()`
}

func (f *fn) errorf(code diag.Code, pos ast.Pos, format string, args ...any) {
	f.c.errorf(code, f.file, pos, format, args...)
}

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
	f.pushScope(body)
	end := f.block(st, body)
	f.popScope()
	if !end.dead {
		f.normalReturn(end, body.Pos)
	}
	if f.hasUnsafe && fd.Contract == nil {
		f.errorf("UNSAFE_MISSING_CONTRACT", fd.Pos, "function %s contains unsafe but has no contract block", fd.Name)
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
