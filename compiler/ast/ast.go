// Package ast is the canonical abstract syntax tree produced by compiler/parser
// (spec/grammar.ebnf). Every node carries a byte-offset span.
package ast

// Pos is a half-open byte span.
type Pos struct{ Start, End int }

// Span returns the node's span.
func (p Pos) Span() Pos { return p }

// Node is any syntax node.
type Node interface{ Span() Pos }

// File is one parsed source file.
type File struct {
	Pos
	Name    string
	Module  string
	Imports []*Import
	Decls   []Decl
}

// Import is `import "path"`.
type Import struct {
	Pos
	Path string
}

// Decl is a top-level declaration.
type Decl interface{ Node }

// TypeParam is `T: C1 + C2`.
type TypeParam struct {
	Pos
	Name        string
	Constraints []*TypeName
}

// Field is `name: type` in a struct or named union variant.
type Field struct {
	Pos
	Name string
	Type TypeExpr
}

// StructDecl is a struct declaration. View marks the reserved `view struct`.
type StructDecl struct {
	Pos
	Name       string
	TypeParams []*TypeParam
	Fields     []*Field
	View       bool
	NoCopy     bool
	Packed     bool // no padding; field access is an unsafe operation
}

// Variant is one union variant; Fields may be unnamed (Name == "").
type Variant struct {
	Pos
	Name   string
	Fields []*Field
}

// UnionDecl is a tagged union declaration.
type UnionDecl struct {
	Pos
	Name       string
	TypeParams []*TypeParam
	Variants   []*Variant
}

// InterfaceDecl declares a nominal interface.
type InterfaceDecl struct {
	Pos
	Name       string
	TypeParams []*TypeParam
	Methods    []*FuncDecl // bodies nil
}

// ImplDecl is `impl [TP] Iface for T { }` or `impl [TP] T { }` (inherent).
type ImplDecl struct {
	Pos
	TypeParams []*TypeParam
	Iface      *TypeName // nil for inherent impl
	Target     TypeExpr
	Funcs      []*FuncDecl
	Destructor *Destructor
}

// Destructor is `destructor(edit self) { }`.
type Destructor struct {
	Pos
	Body *Block
}

// Conv is a parameter convention.
type Conv int

// Parameter conventions. ConvValue is a plain by-value parameter.
const (
	ConvValue Conv = iota
	ConvView
	ConvEdit
	ConvOwn
	ConvOut
)

// Param is one function parameter. For a receiver, Name is "self" and Type nil.
type Param struct {
	Pos
	Name string
	Conv Conv
	Type TypeExpr // nil for receiver
	Self bool
}

// Contract is a contract block (spec §6).
type Contract struct {
	Pos
	Invariant []string
	Requires  []string
	Ensures   []string
}

// FuncDecl declares a function, method, interface method or extern function.
type FuncDecl struct {
	Pos
	Contract   *Contract
	Name       string
	TypeParams []*TypeParam
	Params     []*Param
	Ret        []TypeExpr // 0 = unit, 1 = type, >1 = tuple of accesses
	Where      []*TypeParam // `where T: Copy, ...`: constraints on the owner's or the function's type parameters
	Release    string       // release function name, "" if none
	Body       *Block     // nil for interface methods and extern
	Extern     string     // ABI name, e.g. "C", for extern decls
}

// ConstDecl is `const name: T = expr`.
type ConstDecl struct {
	Pos
	Name  string
	Type  TypeExpr
	Value Expr
}

// GlobalDecl is a module-level `thread_local name: T = init` declaration
// (spec §6 TLS): one instance per thread, a Copy type, a literal or zero()
// initializer.
type GlobalDecl struct {
	Pos
	Name  string
	Type  TypeExpr
	Value Expr
}

// AssertDecl is `unsafe assert Marker for T`.
type AssertDecl struct {
	Pos
	Marker string
	Type   *TypeName
}

// TypeExpr is a syntactic type.
type TypeExpr interface{ Node }

// PrimType is a primitive type name (i8..usize, f32, f64, bool).
type PrimType struct {
	Pos
	Name string
}

// TypeName is a possibly qualified, possibly generic nominal type reference.
type TypeName struct {
	Pos
	Pkg  string
	Name string
	Args []TypeExpr
}

// ArrayType is `[N]T`.
type ArrayType struct {
	Pos
	Len  int
	Elem TypeExpr
}

// SliceType is `[]T`.
type SliceType struct {
	Pos
	Elem TypeExpr
}

// AccessType is `view[T]`, `edit[T]`, `view[]T` or `edit[]T`.
type AccessType struct {
	Pos
	Edit bool
	Seq  bool
	Elem TypeExpr
}

// RawPtrType is `RawPtr[T]`.
type RawPtrType struct {
	Pos
	Elem TypeExpr
}

// FuncType is `func(P...) -> R`.
type FuncType struct {
	Pos
	Params  []*Param // Name empty
	Ret     []TypeExpr
	Closure bool // `closure(P...) -> R`: a second-class callable that may capture (spec §4)
}

// UnitType is `()`.
type UnitType struct{ Pos }

// Stmt is a statement.
type Stmt interface{ Node }

// Block is `{ stmts }`.
type Block struct {
	Pos
	Stmts []Stmt
}

// VarDecl is `var x: T [= expr]`.
type VarDecl struct {
	Pos
	Name string
	Type TypeExpr
	Init Expr
}

// ShortDecl is `x := expr`.
type ShortDecl struct {
	Pos
	Name  string
	Value Expr
}

// Assign is `place = expr`.
type Assign struct {
	Pos
	Target Expr
	Value  Expr
}

// With is `with a, b := subject { }`.
type With struct {
	Pos
	Names   []string
	NamePos []Pos
	Subject Expr
	Body    *Block
}

// Arm is one match arm.
type Arm struct {
	Pos
	Wild    bool
	Qual    *TypeName // optional qualifier before the variant name
	Variant string
	Binds   []string
	BindPos []Pos
	Body    *Block
}

// Match is a match statement. Move marks `match move x`.
type Match struct {
	Pos
	Subject Expr
	Move    bool
	Arms    []*Arm
}

// For is `for x in iter { }`, or `for k, v in iter { }` over a type with
// the cursor protocol (spec/types.md §7).
type For struct {
	Pos
	Var     string
	VarPos  Pos
	Var2    string // the second name, or ""
	Var2Pos Pos
	Iter    Expr
	Body    *Block
	// cursor is the loop the resolver desugared a cursor-protocol for
	// into, which the checker and the lowering run instead; unexported so
	// that tree walks see only the source's nodes.
	cursor *Block
}

// Cursor is the desugared loop of a cursor-protocol for, or nil.
func (f *For) Cursor() *Block { return f.cursor }

// SetCursor records the desugared loop of a cursor-protocol for.
func (f *For) SetCursor(b *Block) { f.cursor = b }

// While is `while cond { }`.
type While struct {
	Pos
	Cond Expr
	Body *Block
}

// If is `if cond { } else ...`.
type If struct {
	Pos
	Cond Expr
	Then *Block
	Else Stmt // *If or *Block or nil
}

// Return is `return [expr]`.
type Return struct {
	Pos
	Value Expr
}

// Break is `break`.
type Break struct{ Pos }

// Continue is `continue`.
type Continue struct{ Pos }

// Unsafe is `unsafe { }`.
type Unsafe struct {
	Pos
	Body *Block
}

// ExprStmt is an expression used as a statement.
type ExprStmt struct {
	Pos
	X Expr
}

// Expr is an expression.
type Expr interface{ Node }

// IntLit is an integer literal; Suffix is the type suffix or "". Text is the
// literal's source text without any sign (digits, separators and suffix); it
// is empty for nodes not produced by the parser.
type IntLit struct {
	Pos
	Value  uint64
	Suffix string
	Neg    bool
	Text   string
}

// FloatLit is a float literal. Text is the literal's source text, as for IntLit.
type FloatLit struct {
	Pos
	Value  float64
	Suffix string
	Text   string
}

// StringLit is a string literal.
type StringLit struct {
	Pos
	Value string
}

// BoolLit is true/false.
type BoolLit struct {
	Pos
	Value bool
}

// Ident is a name reference.
type Ident struct {
	Pos
	Name string
}

// Qualified is `pkg.name` where pkg is an imported module.
type Qualified struct {
	Pos
	Pkg  string
	Name string
}

// FieldExpr is `x.name` (field access or method reference).
type FieldExpr struct {
	Pos
	X    Expr
	Name string
}

// IndexExpr is `x[a, b, ...]`: indexing with one expression, or a generic
// instantiation with type-like expressions (resolved later).
type IndexExpr struct {
	Pos
	X    Expr
	Args []Expr
}

// TypeArgExpr wraps a type that appeared in a generic argument list in
// expression position (for example `Box[[]u8]`), where it cannot be an
// ordinary expression.
type TypeArgExpr struct {
	Pos
	Type TypeExpr
}

// SliceExpr is `x[lo:hi]`; Lo/Hi may be nil.
type SliceExpr struct {
	Pos
	X      Expr
	Lo, Hi Expr
}

// Arg is a call argument; Out marks `out x`.
type Arg struct {
	Pos
	Out bool
	X   Expr
}

// CallExpr is `fn(args)`; Fn may be a FieldExpr for method calls.
type CallExpr struct {
	Pos
	Fn   Expr
	Args []*Arg
}

// Binary is a binary operation.
type Binary struct {
	Pos
	Op   string
	L, R Expr
}

// Unary is `-x` or `!x`.
type Unary struct {
	Pos
	Op string
	X  Expr
}

// Try is `x?`.
type Try struct {
	Pos
	X Expr
}

// Move is `move x`.
type Move struct {
	Pos
	Name string
}

// ClosureExpr is `|params| expr` or `|params| { ... }`: a non-escaping
// closure, typed by the closure parameter it is passed to. Expr is the
// expression form as written (kept for formatting); the resolver gives both
// forms a Body (a return of Expr, or Expr as a statement when the closure
// returns nothing).
type ClosureExpr struct {
	Pos
	Params []*Param // Type may be nil (taken from the parameter type)
	Expr   Expr
	Body   *Block
}

// Copy is `copy(x)`.
type Copy struct {
	Pos
	X Expr
}

// Take is `take(place)`.
type Take struct {
	Pos
	Place Expr
}

// Replace is `replace(place, v)`.
type Replace struct {
	Pos
	Place Expr
	Value Expr
}

// Drop is `drop(x)`.
type Drop struct {
	Pos
	Name string
}

// TrapExpr is `trap()`.
type TrapExpr struct{ Pos }

// ConvExpr is `u64(x)` (checked conversion) or `truncate[u8](x)`.
type ConvExpr struct {
	Pos
	To       string
	X        Expr
	Truncate bool
}

// FieldInit is `name: expr` in a struct literal.
type FieldInit struct {
	Pos
	Name  string
	Value Expr
}

// StructLit is `T { f: v }`.
type StructLit struct {
	Pos
	Type   *TypeName
	Fields []*FieldInit
}

// ArrayLit is `[e1, e2, ...]`: a fixed array of its elements' common type.
type ArrayLit struct {
	Pos
	Elems []Expr
}

// RangeExpr is `lo..hi`.
type RangeExpr struct {
	Pos
	Lo, Hi Expr
}

// UnitLit is `()`.
type UnitLit struct{ Pos }

// Paren is `(x)`.
type Paren struct {
	Pos
	X Expr
}
