package resolve

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

//go:embed prelude/core.kern
var preludeFS embed.FS

// TypeKind distinguishes struct and union declarations.
type TypeKind int

// Type kinds.
const (
	KindStruct TypeKind = iota
	KindUnion
)

// FieldDecl is a struct field or a union variant field (Name may be "").
type FieldDecl struct {
	Name string
	Type Type // may mention the declaring type's TypeParamTypes
	Pos  ast.Pos
}

// VariantDecl is a union variant.
type VariantDecl struct {
	Name   string
	Fields []*FieldDecl
	Index  int
	Pos    ast.Pos
}

// TypeDecl is a resolved struct or union declaration.
type TypeDecl struct {
	Kind                                    TypeKind
	Name                                    string
	Module                                  string
	Pos                                     ast.Pos
	TypeParams                              []*TypeParamType
	Fields                                  []*FieldDecl   // struct
	Variants                                []*VariantDecl // union
	NoCopy                                  bool
	Packed                                  bool
	View                                    bool
	Destructor                              *ast.Destructor
	Methods                                 map[string]*FuncDecl    // inherent and interface methods by name
	Impls                                   map[*InterfaceDecl]bool // interfaces implemented
	AssertSend, AssertShare, AssertInterior bool
	AST                                     ast.Decl
}

// InterfaceDecl is a resolved interface.
type InterfaceDecl struct {
	Name       string
	Module     string
	Pos        ast.Pos
	TypeParams []*TypeParamType
	Methods    map[string]*FuncDecl
	Order      []string
}

// FuncDecl is a resolved function, method or extern declaration.
type FuncDecl struct {
	Name        string
	Module      string
	Pos         ast.Pos
	TypeParams  []*TypeParamType // own type params (not the impl's)
	OwnerParams []*TypeParamType // enclosing impl/type params
	Params      []*ParamDecl
	Ret         []Type
	Release     string
	Where       []*TypeParamType // where-clause bounds on the owner's type parameters, checked at each call
	Contract    *ast.Contract
	Extern      string
	Receiver    *TypeDecl // non-nil for methods
	RecvIface   *InterfaceDecl
	Intrinsic   bool // declared without body in the prelude
	AST         *ast.FuncDecl
	Type        *Func // function type with receiver as first param when a method
}

// ParamDecl is one resolved parameter.
type ParamDecl struct {
	Name string
	Conv ast.Conv
	Type Type // for receivers: the receiver type (Named or TypeParamType)
	Self bool
	Pos  ast.Pos
}

// ConstDecl is a resolved constant.
type ConstDecl struct {
	Name   string
	Module string
	Type   Type
	Value  ast.Expr
	Pos    ast.Pos
}

// GlobalDecl is a resolved thread_local declaration. Init is the constant
// initializer: an integer, a float, a bool, or nil for zero().
type GlobalDecl struct {
	Name   string
	Module string
	Type   Type
	Init   any
	Pos    ast.Pos
}

// Module is a resolved module.
type Module struct {
	Name    string
	Types   map[string]*TypeDecl
	Ifaces  map[string]*InterfaceDecl
	Funcs   map[string]*FuncDecl
	Consts  map[string]*ConstDecl
	Globals map[string]*GlobalDecl
	Files   []*ast.File
	Imports map[string]*Module // alias -> module
}

func newModule(name string) *Module {
	return &Module{
		Name:    name,
		Types:   map[string]*TypeDecl{},
		Ifaces:  map[string]*InterfaceDecl{},
		Funcs:   map[string]*FuncDecl{},
		Consts:  map[string]*ConstDecl{},
		Globals: map[string]*GlobalDecl{},
		Imports: map[string]*Module{},
	}
}

// Program is the result of resolving a set of files plus the prelude.
type Program struct {
	Core    *Module
	Main    *Module
	Modules map[string]*Module
	Diags   []diag.Diagnostic
	// Typing results for the checker.
	Info *Info
}

// Info records per-node resolution results.
type Info struct {
	Types map[ast.Expr]Type // type of every typed expression
	// ImportedConsts records `mod.Name` field expressions that denote an
	// imported constant, for the lowering.
	ImportedConsts map[*ast.FieldExpr]*ConstDecl
	Idents         map[*ast.Ident]*Symbol // resolved identifiers
	Calls          map[*ast.CallExpr]*CallInfo
	Funcs          map[*ast.FuncDecl]*FuncDecl
	Decls          map[any]*Symbol   // declaring nodes (ShortDecl, VarDecl, *ParamDecl, *ast.Destructor, name positions) -> symbol
	Consts         map[ast.Expr]bool // expressions that are compile-time constants
	Fields         map[*ast.FieldExpr]*FieldDecl
	Variants       map[*ast.Arm]*VariantDecl
	// Drops records, per exit point, the symbols whose scope ends there in
	// destruction order: access bindings (AccessEnd, with release) and
	// owned locals/parameters that are initialized at that point (Drop).
	// Keys: *ast.Block (fallthrough end), *ast.Return, *ast.Try (error
	// path), *ast.Break, *ast.Continue, *ast.With (binding scope end),
	// *ast.FuncDecl / *ast.Destructor (parameter drops at the function's
	// fallthrough end), *ast.Assign (the previous value of a reassigned
	// whole variable).
	Drops map[any][]*Symbol
	// OutFieldDrops records, per error-path exit (*ast.Return, *ast.Try),
	// the initialized fields of partially initialized out storage to destroy.
	OutFieldDrops map[any]map[*Symbol][]string
	// OwnedSubject marks with/match statements whose subject is an owned
	// value consumed by the statement (a temporary the lowering must own).
	OwnedSubject map[any]bool
}

func newInfo() *Info {
	return &Info{
		Types: map[ast.Expr]Type{}, Idents: map[*ast.Ident]*Symbol{}, Calls: map[*ast.CallExpr]*CallInfo{},
		Funcs: map[*ast.FuncDecl]*FuncDecl{}, Decls: map[any]*Symbol{}, Consts: map[ast.Expr]bool{}, ImportedConsts: map[*ast.FieldExpr]*ConstDecl{},
		Fields: map[*ast.FieldExpr]*FieldDecl{}, Variants: map[*ast.Arm]*VariantDecl{},
		Drops: map[any][]*Symbol{}, OutFieldDrops: map[any]map[*Symbol][]string{}, OwnedSubject: map[any]bool{},
	}
}

// CallInfo describes a resolved call.
type CallInfo struct {
	Fn          *FuncDecl // nil for calls through function values and intrinsics
	Intrinsic   string    // "thread.spawn", "scope.spawn", "" otherwise
	TypeArgs    map[string]Type
	Method      bool        // receiver is Args' implicit first argument (the FieldExpr.X)
	Params      []FuncParam // instantiated parameter list (receiver first for methods)
	Ret         []Type
	Release     bool
	MultiAccess bool
}

// SymbolKind classifies a symbol.
type SymbolKind int

// Symbol kinds.
const (
	SymLocal  SymbolKind = iota // owned local variable (var/:=/for-copy/arm-owned/match-move)
	SymParam                    // function parameter
	SymAccess                   // with/match/for binding holding an access
	SymFunc
	SymType
	SymConst
	SymGlobal
	SymModule
	SymTypeParam
)

// Symbol is a named entity.
type Symbol struct {
	Kind   SymbolKind
	Name   string
	Type   Type     // for locals/params/access bindings
	Conv   ast.Conv // for params and access bindings (view/edit/own/out)
	Pos    ast.Pos
	Func   *FuncDecl
	TDecl  *TypeDecl
	Iface  *InterfaceDecl
	Const  *ConstDecl
	Global *GlobalDecl
	Module *Module
	TParam *TypeParamType
	ID     int
	File   string // declaring file (locals, parameters, bindings)
	// Access bindings: the root symbol and path they derive from (set by checker).
}

// Resolve parses the prelude and resolves the given files as one module,
// for a debug build.
func Resolve(files []*source.File) *Program { return ResolveFor(files, false) }

// ResolveFor is Resolve for a debug or a release build: the two differ only
// in the prelude's mode lines (Prelude).
func ResolveFor(files []*source.File, release bool) *Program {
	prog := &Program{Modules: map[string]*Module{}, Info: newInfo()}
	r := &resolver{prog: prog}
	core := r.loadPrelude(release)
	prog.Core = core
	prog.Modules["core"] = core
	// Files are grouped by module (spec/types.md §1: one directory is one
	// module; a request may carry several directories). Files in one
	// directory must agree on the module name (MODULE_MISMATCH).
	type modFiles struct{ mod *Module }
	byName := map[string]*modFiles{}
	var order []string
	byDir := map[string]string{}
	for _, f := range files {
		af, ds := parser.Parse(f)
		prog.Diags = append(prog.Diags, ds...)
		if af == nil {
			continue
		}
		dir := filepath.Dir(f.Name)
		if prev, ok := byDir[dir]; ok && prev != af.Module {
			prog.Diags = append(prog.Diags, diag.New("MODULE_MISMATCH", f.Name, diag.Span{Start: af.Start, End: af.Start + 6}, "file declares module %q, expected %q", af.Module, prev))
			continue
		}
		byDir[dir] = af.Module
		mf, ok := byName[af.Module]
		if !ok {
			mf = &modFiles{mod: newModule(af.Module)}
			byName[af.Module] = mf
			order = append(order, af.Module)
		}
		mf.mod.Files = append(mf.mod.Files, af)
	}
	if len(prog.Diags) > 0 || len(order) == 0 {
		return prog
	}
	// dependency order: a module is resolved after the modules it imports
	resolved := map[string]bool{"core": true}
	var sorted []string
	var visit func(name string, stack map[string]bool)
	visit = func(name string, stack map[string]bool) {
		if resolved[name] || stack[name] {
			return
		}
		stack[name] = true
		for _, f := range byName[name].mod.Files {
			for _, imp := range f.Imports {
				alias := imp.Path[strings.LastIndex(imp.Path, "/")+1:]
				if _, ok := byName[alias]; ok {
					visit(alias, stack)
				}
			}
		}
		resolved[name] = true
		sorted = append(sorted, name)
	}
	for _, name := range order {
		visit(name, map[string]bool{})
	}
	// the main module: the one that declares main, else the last one given
	mainName := order[len(order)-1]
	for _, name := range order {
		for _, f := range byName[name].mod.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name == "main" {
					mainName = name
				}
			}
		}
	}
	for _, name := range sorted {
		m := byName[name].mod
		prog.Modules[name] = m
		r.collect(m)
		if len(prog.Diags) > 0 {
			return prog
		}
	}
	for _, name := range sorted {
		r.checkBodies(byName[name].mod)
	}
	prog.Main = byName[mainName].mod
	diag.Sort(prog.Diags)
	return prog
}

type resolver struct {
	globalSyms       map[*GlobalDecl]*Symbol
	prog             *Program
	core             *Module
	deferConstraints bool
	pending          []pendingConstraint
	pendingFFI       []pendingFFI
	pendingPacked    []pendingPacked
}

// pendingPacked is an instantiation of a packed generic struct whose fields
// are checked for an inline Atomic once every declaration is resolved.
type pendingPacked struct {
	file string
	pos  ast.Pos
	n    *Named
}

type pendingFFI struct {
	file string
	fd   *FuncDecl
}

type pendingConstraint struct {
	file string
	pos  ast.Pos
	t    Type
	tp   *TypeParamType
}

func (r *resolver) errorf(code diag.Code, file string, pos ast.Pos, format string, args ...any) {
	r.prog.Diags = append(r.prog.Diags, diag.New(code, file, diag.Span{Start: pos.Start, End: pos.End}, format, args...))
}

// fix attaches a machine-applicable edit to the diagnostic just reported.
func (r *resolver) fix(ops ...diag.StructuredEditOp) {
	if n := len(r.prog.Diags); n > 0 {
		r.prog.Diags[n-1].Fix = &diag.StructuredEdit{Ops: ops}
	}
}

// PreludeSource returns the text of the embedded prelude (std/core.kern)
// for a debug build.
func PreludeSource() []byte { return Prelude(false) }

// Prelude returns the prelude text for a debug or a release build. A line
// ending in //@debug is kept in debug builds only and one ending in
// //@release in release builds only (handles carry identity tags in debug
// builds alone, spec §5.6); the other mode's lines become spaces, so every
// offset, and with it every span, is the same in both texts.
func Prelude(release bool) []byte {
	b, err := preludeFS.ReadFile("prelude/core.kern")
	if err != nil {
		panic("prelude missing: " + err.Error())
	}
	drop := []byte("//@release")
	if release {
		drop = []byte("//@debug")
	}
	lines := bytes.SplitAfter(b, []byte("\n"))
	for _, ln := range lines {
		if bytes.HasSuffix(bytes.TrimRight(ln, "\r\n"), drop) {
			for i := range bytes.TrimRight(ln, "\r\n") {
				ln[i] = ' '
			}
		}
	}
	return bytes.Join(lines, nil)
}

func (r *resolver) loadPrelude(release bool) *Module {
	f := &source.File{Name: "std/core.kern", Bytes: Prelude(release)}
	af, ds := parser.ParseWith(f, parser.Options{AllowBodyless: true})
	if len(ds) > 0 {
		panic("prelude does not parse: " + ds[0].Message)
	}
	core := newModule("core")
	core.Files = []*ast.File{af}
	r.core = core
	r.collect(core)
	if len(r.prog.Diags) > 0 {
		panic("prelude does not resolve: " + r.prog.Diags[0].Message)
	}
	// the library containers have bodies (kern code over the raw-pointer core)
	r.checkBodies(core)
	if len(r.prog.Diags) > 0 {
		d := r.prog.Diags[0]
		panic(fmt.Sprintf("prelude does not resolve: %s (%s at %d-%d)", d.Message, d.Code, d.Span.Start, d.Span.End))
	}
	return core
}

// collect gathers declarations of a module in two passes: names, then
// signatures and fields (which may refer to any name).
func (r *resolver) collect(m *Module) {
	if m != r.core {
		for name, t := range r.core.Types {
			m.Types[name] = t
		}
		for name, i := range r.core.Ifaces {
			m.Ifaces[name] = i
		}
		for name, f := range r.core.Funcs {
			m.Funcs[name] = f
		}
	}
	type pending struct {
		file string
		decl ast.Decl
	}
	var impls, funcs, consts, asserts []pending
	var globals []pending
	for _, f := range m.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.StructDecl:
				if r.dup(m, f.Name, d.Name, d.Pos) {
					continue
				}
				m.Types[d.Name] = &TypeDecl{Kind: KindStruct, Name: d.Name, Module: m.Name, Pos: d.Pos, NoCopy: d.NoCopy, Packed: d.Packed, View: d.View, Methods: map[string]*FuncDecl{}, Impls: map[*InterfaceDecl]bool{}, AST: d}
			case *ast.UnionDecl:
				if r.dup(m, f.Name, d.Name, d.Pos) {
					continue
				}
				m.Types[d.Name] = &TypeDecl{Kind: KindUnion, Name: d.Name, Module: m.Name, Pos: d.Pos, Methods: map[string]*FuncDecl{}, Impls: map[*InterfaceDecl]bool{}, AST: d}
			case *ast.InterfaceDecl:
				if r.dup(m, f.Name, d.Name, d.Pos) {
					continue
				}
				m.Ifaces[d.Name] = &InterfaceDecl{Name: d.Name, Module: m.Name, Pos: d.Pos, Methods: map[string]*FuncDecl{}}
			case *ast.ImplDecl:
				impls = append(impls, pending{f.Name, d})
			case *ast.FuncDecl:
				funcs = append(funcs, pending{f.Name, d})
			case *ast.ConstDecl:
				consts = append(consts, pending{f.Name, d})
			case *ast.GlobalDecl:
				globals = append(globals, pending{f.Name, d})
			case *ast.AssertDecl:
				asserts = append(asserts, pending{f.Name, d})
			}
		}
	}
	// imports
	for _, f := range m.Files {
		for _, imp := range f.Imports {
			alias := imp.Path[strings.LastIndex(imp.Path, "/")+1:]
			if mod, ok := r.prog.Modules[alias]; ok {
				m.Imports[alias] = mod
			} else if alias != "core" {
				r.errorf("NAME_UNRESOLVED", f.Name, imp.Pos, "module %q is not available", imp.Path)
			}
		}
	}
	// second pass: type parameters of every declaration, so that fields may
	// refer to generic types declared later
	for _, f := range m.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.StructDecl:
				if td := m.Types[d.Name]; td != nil && td.AST == d {
					td.TypeParams = r.typeParams(m, f.Name, d.TypeParams, d.Name)
				}
			case *ast.UnionDecl:
				if td := m.Types[d.Name]; td != nil && td.AST == d {
					td.TypeParams = r.typeParams(m, f.Name, d.TypeParams, d.Name)
				}
			case *ast.InterfaceDecl:
				if id := m.Ifaces[d.Name]; id != nil {
					id.TypeParams = r.typeParams(m, f.Name, d.TypeParams, d.Name)
				}
			}
		}
	}
	// third pass: fields, variants and interface methods; constraint checks
	// on instantiations are deferred until implementations and marker
	// assertions have been collected
	r.deferConstraints = true
	for _, f := range m.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.StructDecl:
				td := m.Types[d.Name]
				if td == nil || td.AST != d {
					continue
				}
				sc := r.typeScope(m, td.TypeParams)
				for _, fl := range d.Fields {
					td.Fields = append(td.Fields, &FieldDecl{Name: fl.Name, Type: r.resolveType(m, f.Name, sc, fl.Type), Pos: fl.Pos})
				}
			case *ast.UnionDecl:
				td := m.Types[d.Name]
				if td == nil || td.AST != d {
					continue
				}
				sc := r.typeScope(m, td.TypeParams)
				if len(d.Variants) > 65536 {
					r.errorf("TYPE_TOO_MANY_VARIANTS", f.Name, d.Pos, "union %s has %d variants", d.Name, len(d.Variants))
				}
				for i, v := range d.Variants {
					vd := &VariantDecl{Name: v.Name, Index: i, Pos: v.Pos}
					for _, fl := range v.Fields {
						vd.Fields = append(vd.Fields, &FieldDecl{Name: fl.Name, Type: r.resolveType(m, f.Name, sc, fl.Type), Pos: fl.Pos})
					}
					td.Variants = append(td.Variants, vd)
				}
			case *ast.InterfaceDecl:
				id := m.Ifaces[d.Name]
				if id == nil {
					continue
				}
				sc := r.typeScope(m, id.TypeParams)
				selfT := &TypeParamType{Name: "Self", Owner: d.Name}
				sc.vars["Self"] = &Symbol{Kind: SymTypeParam, Name: "Self", TParam: selfT}
				for _, fn := range d.Methods {
					fd := r.funcSig(m, f.Name, sc, fn, nil, selfT, id.TypeParams)
					fd.RecvIface = id
					if _, dup := id.Methods[fn.Name]; dup {
						r.errorf("NAME_DUPLICATE", f.Name, fn.Pos, "method %s declared twice", fn.Name)
					}
					id.Methods[fn.Name] = fd
					id.Order = append(id.Order, fn.Name)
				}
			}
		}
	}
	for _, p := range funcs {
		d := p.decl.(*ast.FuncDecl)
		if r.dup(m, p.file, d.Name, d.Pos) {
			continue
		}
		sc := r.typeScope(m, nil)
		fd := r.funcSig(m, p.file, sc, d, nil, nil, nil)
		m.Funcs[d.Name] = fd
		r.prog.Info.Funcs[d] = fd
	}
	for _, p := range globals {
		d := p.decl.(*ast.GlobalDecl)
		if r.dup(m, p.file, d.Name, d.Pos) {
			continue
		}
		g := &GlobalDecl{Name: d.Name, Module: m.Name, Type: r.resolveType(m, p.file, r.typeScope(m, nil), d.Type), Pos: d.Pos}
		init, ok := globalInit(g.Type, d.Value)
		if !IsCopy(g.Type) || !ok {
			r.errorf("TLS_INVALID_DECL", p.file, d.Pos, "thread_local %s requires a Copy type and a literal or zero() initializer", d.Name)
			continue
		}
		g.Init = init
		m.Globals[d.Name] = g
	}
	for _, p := range consts {
		d := p.decl.(*ast.ConstDecl)
		if r.dup(m, p.file, d.Name, d.Pos) {
			continue
		}
		m.Consts[d.Name] = &ConstDecl{Name: d.Name, Module: m.Name, Type: r.resolveType(m, p.file, r.typeScope(m, nil), d.Type), Value: d.Value, Pos: d.Pos}
	}
	for _, p := range impls {
		r.collectImpl(m, p.file, p.decl.(*ast.ImplDecl))
	}
	for _, p := range asserts {
		d := p.decl.(*ast.AssertDecl)
		td := m.Types[d.Type.Name]
		if td == nil || td.Module != m.Name {
			r.errorf("NAME_UNRESOLVED", p.file, d.Type.Pos, "assert must name a type declared in this module")
			continue
		}
		switch d.Marker {
		case "Send":
			td.AssertSend = true
		case "Share":
			td.AssertShare = true
		case "Interior":
			td.AssertInterior = true
		default:
			r.errorf("NAME_UNRESOLVED", p.file, d.Pos, "unknown marker %s", d.Marker)
		}
	}
	r.deferConstraints = false
	for _, pc := range r.pending {
		r.checkConstraints(pc.file, pc.pos, pc.t, pc.tp)
	}
	r.pending = nil
	// a packed struct lays its fields out without padding, so an Atomic held
	// inline would lose the natural alignment its instructions need (§4.1)
	for _, f := range m.Files {
		for _, d := range f.Decls {
			sd, ok := d.(*ast.StructDecl)
			if !ok || !sd.Packed {
				continue
			}
			td := m.Types[sd.Name]
			if td == nil || td.AST != sd {
				continue
			}
			for _, fl := range td.Fields {
				if containsAtomic(fl.Type, map[string]bool{}) {
					r.errorf("TYPE_PACKED_ATOMIC", f.Name, fl.Pos, "field %s of packed struct %s holds an Atomic, which must keep its natural alignment", fl.Name, sd.Name)
				}
			}
		}
	}
	for _, pp := range r.pendingPacked {
		r.checkPacked(pp.file, pp.pos, pp.n)
	}
	r.pendingPacked = nil
	for _, pf := range r.pendingFFI {
		r.checkFFI(pf.file, pf.fd)
	}
	r.pendingFFI = nil
}

// globalInit validates a thread_local initializer: an integer, float or bool
// literal of the declared type, or zero().
func globalInit(t Type, e ast.Expr) (any, bool) {
	switch x := e.(type) {
	case *ast.IntLit:
		if p, ok := t.(*Prim); ok && IsInteger(t) && (x.Suffix == "" || x.Suffix == p.Name) {
			lim := intLimits[p.Name]
			if (!x.Neg && x.Value > lim[0]) || (x.Neg && x.Value > lim[1]) {
				return nil, false // out of the declared type's range
			}
			if x.Neg {
				return -int64(x.Value), true
			}
			return int64(x.Value), true // fits: at most the signed maximum, or an unsigned value whose bits are kept
		}
	case *ast.FloatLit:
		if p, ok := t.(*Prim); ok && IsFloat(t) && (x.Suffix == "" || x.Suffix == p.Name) {
			return x.Value, true
		}
	case *ast.BoolLit:
		if IsBool(t) {
			return x.Value, true
		}
	case *ast.CallExpr:
		if id, ok := x.Fn.(*ast.Ident); ok && id.Name == "zero" && len(x.Args) == 0 && IsZeroable(t) {
			return nil, true
		}
	}
	return nil, false
}

func (r *resolver) dup(m *Module, file, name string, pos ast.Pos) bool {
	if t, ok := m.Types[name]; ok && t.Module == m.Name {
		r.errorf("NAME_DUPLICATE", file, pos, "%s is already declared", name)
		return true
	}
	if i, ok := m.Ifaces[name]; ok && i.Module == m.Name {
		r.errorf("NAME_DUPLICATE", file, pos, "%s is already declared", name)
		return true
	}
	if f, ok := m.Funcs[name]; ok && f.Module == m.Name {
		r.errorf("NAME_DUPLICATE", file, pos, "%s is already declared", name)
		return true
	}
	if _, ok := m.Consts[name]; ok {
		r.errorf("NAME_DUPLICATE", file, pos, "%s is already declared", name)
		return true
	}
	return false
}

func (r *resolver) typeParams(m *Module, file string, tps []*ast.TypeParam, owner string) []*TypeParamType {
	var out []*TypeParamType
	for _, tp := range tps {
		t := &TypeParamType{Name: tp.Name, Owner: owner}
		for _, c := range tp.Constraints {
			switch c.Name {
			case "Copy", "Send", "Share":
				t.Constraints = append(t.Constraints, &Constraint{Marker: c.Name})
			default:
				if id, ok := m.Ifaces[c.Name]; ok {
					t.Constraints = append(t.Constraints, &Constraint{Iface: id})
				} else {
					r.errorf("NAME_UNRESOLVED", file, c.Pos, "unknown constraint %s", c.Name)
				}
			}
		}
		out = append(out, t)
	}
	return out
}

// scope is a lexical scope for names.
type scope struct {
	parent *scope
	vars   map[string]*Symbol
	module *Module
}

func (r *resolver) typeScope(m *Module, tps []*TypeParamType) *scope {
	sc := &scope{vars: map[string]*Symbol{}, module: m}
	for _, tp := range tps {
		sc.vars[tp.Name] = &Symbol{Kind: SymTypeParam, Name: tp.Name, TParam: tp}
	}
	return sc
}

func (s *scope) lookup(name string) *Symbol {
	for sc := s; sc != nil; sc = sc.parent {
		if sym, ok := sc.vars[name]; ok {
			return sym
		}
	}
	return nil
}

func (s *scope) child() *scope {
	return &scope{parent: s, vars: map[string]*Symbol{}, module: s.module}
}

// resolveType converts a syntactic type to a Type.
func (r *resolver) resolveType(m *Module, file string, sc *scope, t ast.TypeExpr) Type {
	switch t := t.(type) {
	case nil:
		return &Unit{}
	case *ast.PrimType:
		return &Prim{Name: t.Name}
	case *ast.UnitType:
		return &Unit{}
	case *ast.ArrayType:
		return &Array{Len: t.Len, Elem: r.resolveType(m, file, sc, t.Elem)}
	case *ast.SliceType:
		return &Slice{Elem: r.resolveType(m, file, sc, t.Elem)}
	case *ast.AccessType:
		return &Access{Edit: t.Edit, Seq: t.Seq, Elem: r.resolveType(m, file, sc, t.Elem)}
	case *ast.RawPtrType:
		return &RawPtr{Elem: r.resolveType(m, file, sc, t.Elem)}
	case *ast.FuncType:
		f := &Func{}
		for _, p := range t.Params {
			f.Params = append(f.Params, FuncParam{Conv: p.Conv, Type: r.resolveType(m, file, sc, p.Type)})
		}
		for _, rt := range t.Ret {
			f.Ret = append(f.Ret, r.resolveType(m, file, sc, rt))
		}
		return f
	case *ast.TypeName:
		return r.resolveTypeName(m, file, sc, t)
	}
	r.errorf("TYPE_MISMATCH", file, t.Span(), "unsupported type syntax")
	return &Unit{}
}

func (r *resolver) resolveTypeName(m *Module, file string, sc *scope, t *ast.TypeName) Type {
	if t.Pkg != "" {
		mod, ok := m.Imports[t.Pkg]
		if !ok {
			r.errorf("NAME_UNRESOLVED", file, t.Pos, "unknown module %s", t.Pkg)
			return &Unit{}
		}
		td, ok := mod.Types[t.Name]
		if !ok {
			r.errorf("NAME_UNRESOLVED", file, t.Pos, "unknown type %s.%s", t.Pkg, t.Name)
			return &Unit{}
		}
		if !isExported(t.Name) && mod.Name != "core" {
			r.errorf("NAME_PRIVATE", file, t.Pos, "%s.%s is private to module %s", t.Pkg, t.Name, mod.Name)
		}
		return r.instantiate(m, file, sc, td, t)
	}
	if sym := sc.lookup(t.Name); sym != nil && sym.Kind == SymTypeParam {
		if len(t.Args) > 0 {
			r.errorf("TYPE_ARITY", file, t.Pos, "type parameter %s takes no arguments", t.Name)
		}
		return sym.TParam
	}
	td, ok := m.Types[t.Name]
	if !ok {
		if _, isIface := m.Ifaces[t.Name]; isIface {
			r.errorf("TYPE_MISMATCH", file, t.Pos, "interface %s is not a type", t.Name)
		} else {
			r.errorf("NAME_UNRESOLVED", file, t.Pos, "unknown type %s", t.Name)
		}
		return &Unit{}
	}
	return r.instantiate(m, file, sc, td, t)
}

func (r *resolver) instantiate(m *Module, file string, sc *scope, td *TypeDecl, t *ast.TypeName) Type {
	if len(t.Args) != len(td.TypeParams) {
		r.errorf("TYPE_ARITY", file, t.Pos, "%s expects %d type arguments, got %d", td.Name, len(td.TypeParams), len(t.Args))
		return &Unit{}
	}
	n := &Named{Decl: td}
	for i, a := range t.Args {
		at := r.resolveType(m, file, sc, a)
		n.Args = append(n.Args, at)
		r.checkConstraints(file, a.Span(), at, td.TypeParams[i])
	}
	if len(n.Args) > 0 {
		if r.deferConstraints {
			r.pendingPacked = append(r.pendingPacked, pendingPacked{file, t.Pos, n})
		} else {
			r.checkPacked(file, t.Pos, n)
		}
	}
	return n
}

// checkPacked reports TYPE_PACKED_ATOMIC for a generic instantiation whose
// arguments put an Atomic inline in a packed struct it holds inline: the
// instantiated type itself, or a packed struct reachable through its
// fields (a non-packed wrapper around a packed generic). The declaration
// check covers fields whose types name an Atomic directly, so only
// Atomics that arrive through substitution are reported here.
func (r *resolver) checkPacked(file string, pos ast.Pos, n *Named) {
	if p, field := packedAtomic(n, map[string]bool{}); p != nil {
		r.errorf("TYPE_PACKED_ATOMIC", file, pos, "%s holds an Atomic in field %s of packed struct %s, which must keep its natural alignment", n, field, p)
	}
}

// packedAtomic finds a packed struct reachable from t (inline, behind a
// raw pointer or access, or as the contents of an out-of-line library
// object: a packed value on the heap is no better aligned) whose
// substituted fields hold an Atomic inline
// while its declared field types do not, returning it and the field.
func packedAtomic(t Type, visiting map[string]bool) (*Named, string) {
	switch x := t.(type) {
	case *Array:
		return packedAtomic(x.Elem, visiting)
	case *Slice:
		return packedAtomic(x.Elem, visiting)
	case *Access:
		return packedAtomic(x.Elem, visiting) // the referent's layout is the pointee's
	case *RawPtr:
		return packedAtomic(x.Elem, visiting)
	case *Tuple:
		for _, e := range x.Elems {
			if p, f := packedAtomic(e, visiting); p != nil {
				return p, f
			}
		}
	case *Named:
		if outOfLine(x) {
			for _, a := range x.Args {
				if p, f := packedAtomic(a, visiting); p != nil {
					return p, f
				}
			}
			return nil, ""
		}
		if x.Decl.Kind != KindStruct && x.Decl.Kind != KindUnion {
			return nil, ""
		}
		key := x.String()
		if visiting[key] || expansionDepth(visiting, x.Decl) > maxExpansion {
			// a cycle, or an expanding recursive instantiation (rejected at
			// declaration time, TYPE_RECURSIVE_EXPANSION): stop as markerOf does
			return nil, ""
		}
		visiting[key] = true
		defer delete(visiting, key) // visiting is the active path: siblings do not count as nesting
		args := typeArgs(x)
		if x.Decl.Packed {
			for _, f := range x.Decl.Fields {
				if !containsAtomic(f.Type, map[string]bool{}) && containsAtomic(Subst(f.Type, args), map[string]bool{}) {
					return x, f.Name
				}
			}
		}
		switch x.Decl.Kind {
		case KindStruct:
			for _, f := range x.Decl.Fields {
				if p, fn := packedAtomic(Subst(f.Type, args), visiting); p != nil {
					return p, fn
				}
			}
		case KindUnion:
			for _, v := range x.Decl.Variants {
				for _, ft := range VariantFieldTypes(x, v) {
					if p, fn := packedAtomic(ft, visiting); p != nil {
						return p, fn
					}
				}
			}
		}
	}
	return nil, ""
}

// outOfLine reports the library objects that keep their contents out of
// line (the containers, Shared, Mutex and the runtime handles): nothing
// inside them is laid out in the holding struct. Every other named type,
// the prelude's Option and Result included, holds its fields inline.
func outOfLine(n *Named) bool {
	if n.Decl.Module != "core" {
		return false
	}
	switch n.Decl.Name {
	case "Box", "Vec", "Pool", "Arena", "Shared", "Mutex", "Condvar", "Slab", "Thread", "Join", "Scope", "SystemAllocator":
		return true
	}
	return false
}

// containsAtomic reports whether a value of type t holds an Atomic cell
// inline: directly, or through arrays, tuples and the fields of structs and
// unions; the out-of-line library objects do not count.
func containsAtomic(t Type, visiting map[string]bool) bool {
	switch x := t.(type) {
	case *Array:
		return containsAtomic(x.Elem, visiting)
	case *Tuple:
		for _, e := range x.Elems {
			if containsAtomic(e, visiting) {
				return true
			}
		}
	case *Named:
		if x.Decl.Module == "core" && x.Decl.Name == "Atomic" {
			return true
		}
		if outOfLine(x) {
			return false
		}
		key := x.String()
		if visiting[key] || expansionDepth(visiting, x.Decl) > maxExpansion {
			return false // a cycle or an expanding recursive instantiation, as in markerOf
		}
		visiting[key] = true
		defer delete(visiting, key) // the active path only
		args := typeArgs(x)
		switch x.Decl.Kind {
		case KindStruct:
			for _, f := range x.Decl.Fields {
				if containsAtomic(Subst(f.Type, args), visiting) {
					return true
				}
			}
		case KindUnion:
			for _, v := range x.Decl.Variants {
				for _, ft := range VariantFieldTypes(x, v) {
					if containsAtomic(ft, visiting) {
						return true
					}
				}
			}
		}
	}
	return false
}

// checkConstraints reports CONSTRAINT_UNSATISFIED when at does not satisfy tp.
func (r *resolver) checkConstraints(file string, pos ast.Pos, at Type, tp *TypeParamType) {
	if r.deferConstraints {
		r.pending = append(r.pending, pendingConstraint{file, pos, at, tp})
		return
	}
	for _, c := range tp.Constraints {
		if !r.satisfies(at, c) {
			what := c.Marker
			if c.Iface != nil {
				what = c.Iface.Name
			}
			r.errorf("CONSTRAINT_UNSATISFIED", file, pos, "%s does not satisfy %s", at, what)
		}
	}
}

func (r *resolver) satisfies(t Type, c *Constraint) bool {
	if tp, ok := t.(*TypeParamType); ok {
		for _, cc := range tp.Constraints {
			if cc.Marker == c.Marker && cc.Iface == c.Iface {
				return true
			}
		}
		return false
	}
	switch c.Marker {
	case "Copy":
		return IsCopy(t)
	case "Send":
		return IsSend(t)
	case "Share":
		return IsShare(t)
	}
	n, ok := t.(*Named)
	if !ok {
		if p, ok := t.(*Prim); ok {
			return r.primImpls(p.Name, c.Iface)
		}
		return false
	}
	return n.Decl.Impls[c.Iface]
}

func (r *resolver) primImpls(prim string, iface *InterfaceDecl) bool {
	td, ok := r.core.Types["prim_"+prim]
	return ok && td.Impls[iface]
}

// funcSig resolves a function signature. recv is the receiver type for
// methods (nil otherwise); selfT is the Self type parameter for interfaces.
func (r *resolver) funcSig(m *Module, file string, outer *scope, d *ast.FuncDecl, recv Type, selfT *TypeParamType, ownerParams []*TypeParamType) *FuncDecl {
	fd := &FuncDecl{Name: d.Name, Module: m.Name, Pos: d.Pos, Release: d.Release, Contract: d.Contract, Extern: d.Extern, AST: d, OwnerParams: ownerParams}
	fd.TypeParams = r.typeParams(m, file, d.TypeParams, d.Name)
	sc := outer.child()
	for _, tp := range fd.TypeParams {
		sc.vars[tp.Name] = &Symbol{Kind: SymTypeParam, Name: tp.Name, TParam: tp}
	}
	// A where clause adds constraints to one of the function's own type
	// parameters (then it is simply part of that parameter) or to one of
	// the owner's (spec/types.md): the body sees the parameter with the
	// added constraints, and every call checks the bound on the owner's
	// argument, as it checks the parameters' own constraints.
	if len(d.Where) > 0 && selfT != nil {
		// a bound on an interface method would be bypassed by a call through
		// the interface's constraint, which knows only the interface's signature
		r.errorf("TYPE_MISMATCH", file, d.Where[0].Pos, "where clause on interface method %s: an interface's methods are unconditional", d.Name)
		d.Where = nil
	}
	strengthened := map[string]Type{}
	for i, b := range r.typeParams(m, file, d.Where, d.Name) {
		var own, owner *TypeParamType
		for _, tp := range fd.TypeParams {
			if tp.Name == b.Name {
				own = tp
			}
		}
		for _, tp := range ownerParams {
			if tp.Name == b.Name {
				owner = tp
			}
		}
		switch {
		case own != nil:
			own.Constraints = append(own.Constraints, b.Constraints...)
		case owner != nil:
			bound := &TypeParamType{Name: owner.Name, Owner: owner.Owner, Constraints: b.Constraints}
			fd.Where = append(fd.Where, bound)
			seen := &TypeParamType{Name: owner.Name, Owner: owner.Owner, Constraints: append(append([]*Constraint{}, owner.Constraints...), b.Constraints...)}
			sc.vars[b.Name] = &Symbol{Kind: SymTypeParam, Name: b.Name, TParam: seen}
			strengthened[b.Name] = seen
		default:
			r.errorf("NAME_UNRESOLVED", file, d.Where[i].Pos, "where clause names %s, which is not a type parameter of %s or its owner", b.Name, d.Name)
		}
	}
	if len(strengthened) > 0 && recv != nil {
		// the receiver's fields see the strengthened parameter too; the
		// parameter is Identical to the owner's (same name and owner), so
		// the method still matches every call
		recv = Subst(recv, strengthened)
	}
	ft := &Func{}
	for _, p := range d.Params {
		pd := &ParamDecl{Name: p.Name, Conv: p.Conv, Self: p.Self, Pos: p.Pos}
		if p.Self {
			if recv != nil {
				pd.Type = recv
			} else if selfT != nil {
				pd.Type = selfT
			} else {
				r.errorf("SYNTAX", file, p.Pos, "self outside impl or interface")
				pd.Type = &Unit{}
			}
		} else {
			pd.Type = r.resolveType(m, file, sc, p.Type)
			if a, ok := pd.Type.(*Access); ok {
				if a.Edit {
					pd.Conv = ast.ConvEdit
				} else {
					pd.Conv = ast.ConvView
				}
			}
		}
		fd.Params = append(fd.Params, pd)
		ft.Params = append(ft.Params, FuncParam{Conv: pd.Conv, Type: pd.Type})
	}
	for _, rt := range d.Ret {
		fd.Ret = append(fd.Ret, r.resolveType(m, file, sc, rt))
	}
	ft.Ret = fd.Ret
	fd.Type = ft
	if d.Body == nil && d.Extern == "" {
		fd.Intrinsic = true
	}
	if d.Extern != "" {
		r.pendingFFI = append(r.pendingFFI, pendingFFI{file, fd})
	}
	return fd
}

func (r *resolver) checkFFI(file string, fd *FuncDecl) {
	for _, p := range fd.Params {
		if p.Conv != ast.ConvValue || !ffiType(p.Type) {
			r.errorf("FFI_UNSUPPORTED_TYPE", file, p.Pos, "parameter %s has a type not permitted in extern \"C\"", p.Name)
		}
	}
	for _, rt := range fd.Ret {
		if !ffiType(rt) {
			r.errorf("FFI_UNSUPPORTED_TYPE", file, fd.Pos, "return type %s is not permitted in extern \"C\"", rt)
		}
	}
}

func ffiType(t Type) bool {
	switch x := t.(type) {
	case *Prim, *RawPtr, *Unit:
		return true
	case *Named:
		if x.Decl.Kind != KindStruct || !IsCopy(t) {
			return false
		}
		for _, f := range x.Decl.Fields {
			if !ffiType(Subst(f.Type, typeArgs(x))) {
				return false
			}
		}
		return true
	}
	return false
}

// typeArgs maps a Named's declaration parameters to its arguments.
func typeArgs(n *Named) map[string]Type {
	m := map[string]Type{}
	for i, tp := range n.Decl.TypeParams {
		if i < len(n.Args) {
			m[tp.Name] = n.Args[i]
		}
	}
	return m
}

// FieldType returns the instantiated type of a struct field or nil.
func FieldType(n *Named, name string) (*FieldDecl, Type) {
	for _, f := range n.Decl.Fields {
		if f.Name == name {
			return f, Subst(f.Type, typeArgs(n))
		}
	}
	return nil, nil
}

// VariantFieldTypes returns the instantiated field types of a variant.
func VariantFieldTypes(n *Named, v *VariantDecl) []Type {
	var out []Type
	for _, f := range v.Fields {
		out = append(out, Subst(f.Type, typeArgs(n)))
	}
	return out
}

func (r *resolver) collectImpl(m *Module, file string, d *ast.ImplDecl) {
	tps := r.typeParams(m, file, d.TypeParams, "impl")
	sc := r.typeScope(m, tps)
	// Allow `impl Box[T] { }` to introduce T implicitly when it is not declared.
	if tn, ok := d.Target.(*ast.TypeName); ok {
		for _, a := range tn.Args {
			if an, ok := a.(*ast.TypeName); ok && len(an.Args) == 0 && an.Pkg == "" {
				if sc.lookup(an.Name) == nil {
					if _, isType := m.Types[an.Name]; !isType {
						tp := &TypeParamType{Name: an.Name, Owner: "impl"}
						tps = append(tps, tp)
						sc.vars[an.Name] = &Symbol{Kind: SymTypeParam, Name: an.Name, TParam: tp}
					}
				}
			}
		}
	}
	target := r.resolveType(m, file, sc, d.Target)
	var td *TypeDecl
	switch tt := target.(type) {
	case *Named:
		td = tt.Decl
		seenParam := map[string]bool{}
		for i, arg := range tt.Args {
			tp, isParam := arg.(*TypeParamType)
			if !isParam || seenParam[tp.Name] {
				r.errorf("TYPE_MISMATCH", file, d.Target.Span(), "impl targets must be fully generic with distinct type parameters (impl ... for %s[T]); specialized implementations are not supported", td.Name)
				return
			}
			seenParam[tp.Name] = true
			if i < len(td.TypeParams) && len(tp.Constraints) > len(td.TypeParams[i].Constraints) {
				r.errorf("TYPE_MISMATCH", file, d.Target.Span(), "conditional implementations are not supported: %s has constraints beyond those of %s's declaration", tp.Name, td.Name)
				return
			}
		}
	case *Prim:
		name := "prim_" + tt.Name
		if _, ok := r.core.Types[name]; !ok {
			r.core.Types[name] = &TypeDecl{Kind: KindStruct, Name: tt.Name, Module: "core", Methods: map[string]*FuncDecl{}, Impls: map[*InterfaceDecl]bool{}}
		}
		td = r.core.Types[name]
	default:
		r.errorf("TYPE_MISMATCH", file, d.Target.Span(), "impl target must be a struct, union or primitive type")
		return
	}
	if td.Module != m.Name && m != r.core {
		if d.Iface == nil {
			r.errorf("NAME_PRIVATE", file, d.Target.Span(), "inherent impl of a type from another module")
			return
		}
	}
	var iface *InterfaceDecl
	ifaceArgs := map[string]Type{}
	if d.Iface != nil {
		iface = m.Ifaces[d.Iface.Name]
		if iface == nil {
			r.errorf("NAME_UNRESOLVED", file, d.Iface.Pos, "unknown interface %s", d.Iface.Name)
			return
		}
		if len(d.Iface.Args) != len(iface.TypeParams) {
			r.errorf("TYPE_ARITY", file, d.Iface.Pos, "%s expects %d type arguments, got %d", iface.Name, len(iface.TypeParams), len(d.Iface.Args))
			return
		}
		for i, a := range d.Iface.Args {
			at := r.resolveType(m, file, sc, a)
			r.checkConstraints(file, a.Span(), at, iface.TypeParams[i])
			ifaceArgs[iface.TypeParams[i].Name] = at
		}
	}
	if d.Destructor != nil {
		if td.Destructor != nil && td.AST != nil {
			r.errorf("NAME_DUPLICATE", file, d.Destructor.Pos, "type %s already has a destructor", td.Name)
		}
		td.Destructor = d.Destructor
	}
	var selfT *TypeParamType
	if iface != nil {
		selfT = &TypeParamType{Name: "Self", Owner: iface.Name}
	}
	seen := map[string]bool{}
	for _, fn := range d.Funcs {
		fd := r.funcSig(m, file, sc, fn, target, selfT, tps)
		fd.Receiver = td
		fd.RecvIface = iface
		r.prog.Info.Funcs[fn] = fd
		seen[fn.Name] = true
		if iface != nil {
			im, ok := iface.Methods[fn.Name]
			if !ok {
				r.errorf("IMPL_SIGNATURE_MISMATCH", file, fn.Pos, "%s is not a method of %s", fn.Name, iface.Name)
				continue
			}
			if !sigMatches(im, fd, target, ifaceArgs) {
				r.errorf("IMPL_SIGNATURE_MISMATCH", file, fn.Pos, "signature of %s differs from %s.%s", fn.Name, iface.Name, fn.Name)
			}
		}
		if prev, dup := td.Methods[fn.Name]; dup && prev.AST != fn {
			// inherent and interface methods share one namespace (spec/types.md §4)
			r.errorf("NAME_DUPLICATE", file, fn.Pos, "method %s declared twice for %s", fn.Name, td.Name)
			continue
		}
		td.Methods[fn.Name] = fd
	}
	if iface != nil {
		for _, name := range iface.Order {
			if !seen[name] {
				r.errorf("IMPL_INCOMPLETE", file, d.Pos, "impl %s for %s lacks method %s", iface.Name, td.Name, name)
			}
		}
		td.Impls[iface] = true
	}
}

// sigMatches compares an interface method with an impl method, replacing Self
// by the target type.
func sigMatches(im, fd *FuncDecl, target Type, ifaceArgs map[string]Type) bool {
	if len(im.Params) != len(fd.Params) || len(im.Ret) != len(fd.Ret) || len(im.TypeParams) != len(fd.TypeParams) {
		return false
	}
	m := map[string]Type{"Self": target}
	for k, v := range ifaceArgs {
		m[k] = v
	}
	for i, tp := range im.TypeParams {
		// method type parameters are matched positionally
		if len(tp.Constraints) != len(fd.TypeParams[i].Constraints) {
			return false
		}
		for j, c := range tp.Constraints {
			d := fd.TypeParams[i].Constraints[j]
			if c.Marker != d.Marker || c.Iface != d.Iface {
				return false
			}
		}
		m[tp.Name] = fd.TypeParams[i]
	}
	for i := range im.Params {
		a, b := im.Params[i], fd.Params[i]
		if a.Conv != b.Conv || a.Self != b.Self {
			return false
		}
		if a.Self {
			continue
		}
		if !Identical(Subst(a.Type, m), b.Type) {
			return false
		}
	}
	for i := range im.Ret {
		if !Identical(Subst(im.Ret[i], m), fd.Ret[i]) {
			return false
		}
	}
	return true
}

// ---- marker derivation (spec §5.1, §5.4, §5.5) ----

// IsCopy reports whether t is Copy.
func IsCopy(t Type) bool { return markerOf(t, markerCopy, map[string]bool{}) }

// IsZeroable reports whether t has a valid all-zero value (spec/types.md §1):
// Copy, and containing no function type.
func IsZeroable(t Type) bool {
	if !IsCopy(t) {
		return false
	}
	return !containsFunc(t, map[string]bool{})
}

func containsFunc(t Type, visiting map[string]bool) bool {
	switch x := t.(type) {
	case *Func:
		return true
	case *Array:
		return containsFunc(x.Elem, visiting)
	case *Named:
		key := x.String()
		if visiting[key] || expansionDepth(visiting, x.Decl) > maxExpansion {
			return false
		}
		visiting[key] = true
		defer delete(visiting, key)
		args := typeArgs(x)
		for _, f := range x.Decl.Fields {
			if containsFunc(Subst(f.Type, args), visiting) {
				return true
			}
		}
		for _, v := range x.Decl.Variants {
			for _, f := range v.Fields {
				if containsFunc(Subst(f.Type, args), visiting) {
					return true
				}
			}
		}
	}
	return false
}

// IsSend reports whether t is Send.
func IsSend(t Type) bool { return markerOf(t, markerSend, map[string]bool{}) }

// IsShare reports whether t is Share.
func IsShare(t Type) bool { return markerOf(t, markerShare, map[string]bool{}) }

// IsInterior reports whether t is Interior.
func IsInterior(t Type) bool { return markerOf(t, markerInterior, map[string]bool{}) }

// HasDestructor reports whether t or any field type has a destructor.
func HasDestructor(t Type) bool { return markerOf(t, markerDestructor, map[string]bool{}) }

type marker int

const (
	markerCopy marker = iota
	markerSend
	markerShare
	markerInterior
	markerDestructor
)

// maxExpansion bounds how many instantiations of one declaration may be
// nested on the marker traversal path.
const maxExpansion = 8

// expansionDepth counts the instantiations of decl currently on the path.
func expansionDepth(visiting map[string]bool, decl *TypeDecl) int {
	n := 0
	prefix := decl.Name + "["
	for k := range visiting {
		if k == decl.Name || strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// existential reports whether a marker is "true if any field has it"
// (Interior, destructor) rather than "true only if every field has it".
func (mk marker) existential() bool { return mk == markerInterior || mk == markerDestructor }

func hasMarker(tp *TypeParamType, m string) bool {
	for _, c := range tp.Constraints {
		if c.Marker == m {
			return true
		}
	}
	return false
}

// markerOf evaluates a marker with one traversal state shared across nested
// fields, so recursive type graphs terminate.
func markerOf(t Type, mk marker, visiting map[string]bool) bool {
	switch x := t.(type) {
	case *Prim, *Func, *Unit, *Bad:
		return !mk.existential()
	case *RawPtr:
		switch mk {
		case markerCopy:
			return true
		case markerSend, markerShare:
			return false
		}
		return false
	case *Array:
		return markerOf(x.Elem, mk, visiting)
	case *Slice:
		return markerOf(x.Elem, mk, visiting)
	case *TypeParamType:
		switch mk {
		case markerCopy:
			return hasMarker(x, "Copy")
		case markerSend:
			return hasMarker(x, "Send")
		case markerShare:
			return hasMarker(x, "Share")
		}
		return false
	case *Named:
		return namedMarker(x, mk, visiting)
	}
	return false
}

func namedMarker(n *Named, mk marker, visiting map[string]bool) bool {
	d := n.Decl
	key := n.String()
	arg := func(i int) Type {
		if i < len(n.Args) {
			return n.Args[i]
		}
		return &Bad{}
	}
	// Special library rules (spec §5.5): Shared/Mutex/Atomic.
	switch d.Module + "." + d.Name {
	case "core.Shared":
		switch mk {
		case markerShare:
			return markerOf(arg(0), markerShare, visiting)
		case markerSend:
			return markerOf(arg(0), markerSend, visiting) && markerOf(arg(0), markerShare, visiting)
		case markerInterior:
			return true
		case markerCopy:
			return false
		}
	case "core.Mutex":
		switch mk {
		case markerShare, markerSend:
			return markerOf(arg(0), markerSend, visiting)
		case markerInterior:
			return true
		case markerCopy:
			return false
		}
	case "core.Atomic", "core.Condvar":
		switch mk {
		case markerShare, markerSend, markerInterior:
			return true
		case markerCopy:
			return false
		}
	case "core.Box", "core.Vec", "core.Pool", "core.Arena":
		// the containers own their elements through a raw pointer: their
		// Send, Share and Interior markers are the element's (a RawPtr field
		// alone would make them neither Send nor Share)
		switch mk {
		case markerSend, markerShare, markerInterior:
			return markerOf(arg(0), mk, visiting)
		case markerCopy:
			return false
		}
	}
	switch mk {
	case markerCopy:
		if d.NoCopy || d.Destructor != nil {
			return false
		}
	case markerSend:
		if d.AssertSend {
			return true
		}
	case markerShare:
		if d.AssertShare {
			return true
		}
		if d.AssertInterior {
			return false
		}
	case markerInterior:
		if d.AssertInterior {
			return true
		}
	case markerDestructor:
		if d.Destructor != nil {
			return true
		}
	}
	if visiting[key] || expansionDepth(visiting, d) > maxExpansion {
		// a cycle through this instantiation, or an expanding recursive
		// instantiation (A[T] containing A[Option[T]]): assume the universal
		// markers hold and the existential ones do not. Expanding recursion is
		// rejected at declaration time from Phase 1 (TYPE_RECURSIVE_EXPANSION).
		return !mk.existential()
	}
	visiting[key] = true
	defer delete(visiting, key)
	args := typeArgs(n)
	check := func(ft Type) bool { return markerOf(Subst(ft, args), mk, visiting) }
	var fields []*FieldDecl
	fields = append(fields, d.Fields...)
	for _, v := range d.Variants {
		fields = append(fields, v.Fields...)
	}
	for _, f := range fields {
		r := check(f.Type)
		if mk.existential() && r {
			return true
		}
		if !mk.existential() && !r {
			return false
		}
	}
	return !mk.existential()
}

// typeString is a helper for messages.
func typeString(t Type) string {
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

var _ = fmt.Sprintf
