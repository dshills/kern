package ast

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Dump writes a parsed file as one line of S-expressions, the format a
// kern program's parser is compared against (specs/selfhost-parser). Spans
// and the file name are not written. Each node is `(name field ...)` with
// its fields in the order below; a list is `[a b]`; a nil node, or an
// empty optional name, is `_`; a boolean is `t` or `f`; an integer is
// decimal; a name or literal text is written as it is; a string value is
// `x` followed by the lowercase hex of its bytes; a convention is value,
// view, edit, own or out.
//
//	File        (file Module [Imports] [Decls])
//	Import      (import Path:str)
//	TypeParam   (tp Name [Constraints])
//	Field       (field Name|_ Type)
//	StructDecl  (struct Name [TypeParams] [Fields] View NoCopy Packed)
//	Variant     (variant Name [Fields])
//	UnionDecl   (union Name [TypeParams] [Variants])
//	InterfaceDecl (interface Name [TypeParams] [Methods])
//	ImplDecl    (impl [TypeParams] Iface|_ Target [Funcs] Destructor|_)
//	Destructor  (destructor Body)
//	Param       (param Name|_ Conv Type|_ Self)
//	Contract    (contract [Invariant:str] [Requires:str] [Ensures:str])
//	Budget      (budget [Items])
//	BudgetItem  (item Name Value)
//	FuncDecl    (func Name Contract|_ [Budget] [TypeParams] [Params] [Ret] [Where] Release|_ Body|_ Extern:str|_)
//	ConstDecl   (const Name Type Value)
//	GlobalDecl  (global Name Type Value)
//	AssertDecl  (assert Marker Type)
//	PrimType    (prim Name)
//	TypeName    (tname Pkg|_ Name [Args])
//	ArrayType   (array Len Elem)
//	SliceType   (slice Elem)
//	AccessType  (access Edit Seq Elem)
//	RawPtrType  (rawptr Elem)
//	FuncType    (functype [Params] [Ret] Closure)
//	UnitType    (unit)
//	Block       (block [Stmts])
//	VarDecl     (var Name Type Init|_)
//	ShortDecl   (short Name Value)
//	Assign      (assign Target Value)
//	With        (with [Names] Subject Body)
//	Arm         (arm Wild Qual|_ Variant|_ [Binds] Body)
//	Match       (match Subject Move [Arms])
//	For         (for Var Var2|_ Iter Body)
//	While       (while Cond Body)
//	If          (if Cond Then Else|_)
//	Return      (return Value|_)
//	Break       (break)
//	Continue    (continue)
//	Unsafe      (unsafe Body)
//	ExprStmt    (expr X)
//	IntLit      (int Text Value Suffix|_ Neg)
//	FloatLit    (float Text Suffix|_)
//	StringLit   (string Value:str)
//	BoolLit     (bool Value)
//	Ident       (ident Name)
//	Qualified   (qualified Pkg Name)
//	FieldExpr   (fieldx X Name)
//	IndexExpr   (index X [Args])
//	TypeArgExpr (typearg Type)
//	SliceExpr   (slicex X Lo|_ Hi|_)
//	Arg         (arg Out X)
//	CallExpr    (call Fn [Args])
//	Binary      (binary Op L R)
//	Unary       (unary Op X)
//	Try         (try X)
//	Move        (move Name)
//	ClosureExpr (closure [Params] Expr|_ Body|_)
//	Copy        (copy X)
//	Take        (take Place)
//	Replace     (replace Place Value)
//	Drop        (drop Name)
//	TrapExpr    (trap)
//	ConvExpr    (conv To X Truncate)
//	FieldInit   (finit Name Value)
//	StructLit   (structlit Type [Fields])
//	ArrayLit    (arraylit [Elems])
//	RangeExpr   (range Lo Hi)
//	UnitLit     (unitlit)
//	Paren       (paren X)
//
// A FuncDecl's Budget is printed only when present, so a function without
// a budget block dumps as it did before budgets existed.
//
// A node type not in the table panics: a parser change that adds one must
// add its form here and to the kern dumper.
func Dump(f *File) string {
	var d dumper
	d.node(f)
	d.b.WriteByte('\n')
	return d.b.String()
}

// DumpSpans is Dump with positions (specs/selfhost-spans): each node's
// name is followed by its span as @Start:End, so (ident x) is
// (ident@12:13 x), and the extra positions follow their names in the
// same form: a BudgetItem's name, a With's names, an Arm's binds and a
// For's Var and Var2 (an absent Var2 is _ with no position). Removing
// every @Start:End gives Dump: a string value is hex and no name or
// literal text contains @.
func DumpSpans(f *File) string {
	d := dumper{spans: true}
	d.node(f)
	d.b.WriteByte('\n')
	return d.b.String()
}

type dumper struct {
	b     strings.Builder
	spans bool
	pos   Pos // the node being opened
}

func (d *dumper) open(name string) {
	d.b.WriteByte('(')
	d.b.WriteString(name)
	d.at(d.pos)
}

// at writes a position when the dump has spans.
func (d *dumper) at(p Pos) {
	if d.spans {
		d.b.WriteByte('@')
		d.b.WriteString(strconv.Itoa(p.Start))
		d.b.WriteByte(':')
		d.b.WriteString(strconv.Itoa(p.End))
	}
}

// named writes a name and, with spans, its position; an empty name is _
// with no position.
func (d *dumper) named(s string, p Pos) {
	d.word(s)
	if s != "" {
		d.at(p)
	}
}

// nameds writes a list of names, each with its position when the dump has
// spans.
func (d *dumper) nameds(ss []string, ps []Pos) {
	d.sp()
	d.b.WriteByte('[')
	for i, s := range ss {
		if i > 0 {
			d.sp()
		}
		d.b.WriteString(s)
		if i < len(ps) {
			d.at(ps[i])
		}
	}
	d.b.WriteByte(']')
}

func (d *dumper) close() { d.b.WriteByte(')') }
func (d *dumper) sp()    { d.b.WriteByte(' ') }

// word writes a name or literal text, or _ when it is empty.
func (d *dumper) word(s string) {
	d.sp()
	if s == "" {
		d.b.WriteByte('_')
		return
	}
	d.b.WriteString(s)
}

// str writes a string value as x and its hex.
func (d *dumper) str(s string) {
	d.sp()
	d.b.WriteByte('x')
	d.b.WriteString(hex.EncodeToString([]byte(s)))
}

func (d *dumper) flag(v bool) {
	d.sp()
	if v {
		d.b.WriteByte('t')
	} else {
		d.b.WriteByte('f')
	}
}

func (d *dumper) num(v uint64) { d.sp(); d.b.WriteString(strconv.FormatUint(v, 10)) }

func (d *dumper) strs(ss []string) {
	d.sp()
	d.b.WriteByte('[')
	for i, s := range ss {
		if i > 0 {
			d.sp()
		}
		d.b.WriteByte('x')
		d.b.WriteString(hex.EncodeToString([]byte(s)))
	}
	d.b.WriteByte(']')
}

// child writes one optional child node.
func (d *dumper) child(n Node) {
	d.sp()
	d.node(n)
}

// list writes a list of nodes.
func list[T Node](d *dumper, ns []T) {
	d.sp()
	d.b.WriteByte('[')
	for i, n := range ns {
		if i > 0 {
			d.sp()
		}
		d.node(n)
	}
	d.b.WriteByte(']')
}

var convNames = [...]string{ConvValue: "value", ConvView: "view", ConvEdit: "edit", ConvOwn: "own", ConvOut: "out"}

// isNil reports a nil node: a nil interface, or a nil pointer of any node
// type held in one (an absent *Block, *TypeName, *Contract ...).
func isNil(n Node) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func (d *dumper) node(n Node) {
	if isNil(n) {
		d.b.WriteByte('_')
		return
	}
	d.pos = n.Span()
	switch x := n.(type) {
	case *File:
		d.open("file")
		d.word(x.Module)
		list(d, x.Imports)
		list(d, x.Decls)
	case *Import:
		d.open("import")
		d.str(x.Path)
	case *TypeParam:
		d.open("tp")
		d.word(x.Name)
		list(d, x.Constraints)
	case *Field:
		d.open("field")
		d.word(x.Name)
		d.child(x.Type)
	case *StructDecl:
		d.open("struct")
		d.word(x.Name)
		list(d, x.TypeParams)
		list(d, x.Fields)
		d.flag(x.View)
		d.flag(x.NoCopy)
		d.flag(x.Packed)
	case *Variant:
		d.open("variant")
		d.word(x.Name)
		list(d, x.Fields)
	case *UnionDecl:
		d.open("union")
		d.word(x.Name)
		list(d, x.TypeParams)
		list(d, x.Variants)
	case *InterfaceDecl:
		d.open("interface")
		d.word(x.Name)
		list(d, x.TypeParams)
		list(d, x.Methods)
	case *ImplDecl:
		d.open("impl")
		list(d, x.TypeParams)
		d.child(x.Iface)
		d.child(x.Target)
		list(d, x.Funcs)
		d.child(x.Destructor)
	case *Destructor:
		d.open("destructor")
		d.child(x.Body)
	case *Param:
		d.open("param")
		d.word(x.Name)
		d.word(convNames[x.Conv])
		d.child(x.Type)
		d.flag(x.Self)
	case *Contract:
		d.open("contract")
		d.strs(x.Invariant)
		d.strs(x.Requires)
		d.strs(x.Ensures)
	case *Budget:
		d.open("budget")
		list(d, x.Items)
	case *BudgetItem:
		d.open("item")
		d.named(x.Name, x.NamePos)
		d.word(x.Value)
	case *FuncDecl:
		d.open("func")
		d.word(x.Name)
		d.child(x.Contract)
		if x.Budget != nil {
			d.child(x.Budget)
		}
		list(d, x.TypeParams)
		list(d, x.Params)
		list(d, x.Ret)
		list(d, x.Where)
		d.word(x.Release)
		d.child(x.Body)
		if x.Extern == "" {
			d.word("")
		} else {
			d.str(x.Extern)
		}
	case *ConstDecl:
		d.open("const")
		d.word(x.Name)
		d.child(x.Type)
		d.child(x.Value)
	case *GlobalDecl:
		d.open("global")
		d.word(x.Name)
		d.child(x.Type)
		d.child(x.Value)
	case *AssertDecl:
		d.open("assert")
		d.word(x.Marker)
		d.child(x.Type)
	case *PrimType:
		d.open("prim")
		d.word(x.Name)
	case *TypeName:
		d.open("tname")
		d.word(x.Pkg)
		d.word(x.Name)
		list(d, x.Args)
	case *ArrayType:
		d.open("array")
		d.num(uint64(x.Len))
		d.child(x.Elem)
	case *SliceType:
		d.open("slice")
		d.child(x.Elem)
	case *AccessType:
		d.open("access")
		d.flag(x.Edit)
		d.flag(x.Seq)
		d.child(x.Elem)
	case *RawPtrType:
		d.open("rawptr")
		d.child(x.Elem)
	case *FuncType:
		d.open("functype")
		list(d, x.Params)
		list(d, x.Ret)
		d.flag(x.Closure)
	case *UnitType:
		d.open("unit")
	case *Block:
		d.open("block")
		list(d, x.Stmts)
	case *VarDecl:
		d.open("var")
		d.word(x.Name)
		d.child(x.Type)
		d.child(x.Init)
	case *ShortDecl:
		d.open("short")
		d.word(x.Name)
		d.child(x.Value)
	case *Assign:
		d.open("assign")
		d.child(x.Target)
		d.child(x.Value)
	case *With:
		d.open("with")
		d.nameds(x.Names, x.NamePos)
		d.child(x.Subject)
		d.child(x.Body)
	case *Arm:
		d.open("arm")
		d.flag(x.Wild)
		d.child(x.Qual)
		d.word(x.Variant)
		d.nameds(x.Binds, x.BindPos)
		d.child(x.Body)
	case *Match:
		d.open("match")
		d.child(x.Subject)
		d.flag(x.Move)
		list(d, x.Arms)
	case *For:
		d.open("for")
		d.named(x.Var, x.VarPos)
		d.named(x.Var2, x.Var2Pos)
		d.child(x.Iter)
		d.child(x.Body)
	case *While:
		d.open("while")
		d.child(x.Cond)
		d.child(x.Body)
	case *If:
		d.open("if")
		d.child(x.Cond)
		d.child(x.Then)
		d.child(x.Else)
	case *Return:
		d.open("return")
		d.child(x.Value)
	case *Break:
		d.open("break")
	case *Continue:
		d.open("continue")
	case *Unsafe:
		d.open("unsafe")
		d.child(x.Body)
	case *ExprStmt:
		d.open("expr")
		d.child(x.X)
	case *IntLit:
		d.open("int")
		d.word(x.Text)
		d.num(x.Value)
		d.word(x.Suffix)
		d.flag(x.Neg)
	case *FloatLit:
		d.open("float")
		d.word(x.Text)
		d.word(x.Suffix)
	case *StringLit:
		d.open("string")
		d.str(x.Value)
	case *BoolLit:
		d.open("bool")
		d.flag(x.Value)
	case *Ident:
		d.open("ident")
		d.word(x.Name)
	case *Qualified:
		d.open("qualified")
		d.word(x.Pkg)
		d.word(x.Name)
	case *FieldExpr:
		d.open("fieldx")
		d.child(x.X)
		d.word(x.Name)
	case *IndexExpr:
		d.open("index")
		d.child(x.X)
		list(d, x.Args)
	case *TypeArgExpr:
		d.open("typearg")
		d.child(x.Type)
	case *SliceExpr:
		d.open("slicex")
		d.child(x.X)
		d.child(x.Lo)
		d.child(x.Hi)
	case *Arg:
		d.open("arg")
		d.flag(x.Out)
		d.child(x.X)
	case *CallExpr:
		d.open("call")
		d.child(x.Fn)
		list(d, x.Args)
	case *Binary:
		d.open("binary")
		d.word(x.Op)
		d.child(x.L)
		d.child(x.R)
	case *Unary:
		d.open("unary")
		d.word(x.Op)
		d.child(x.X)
	case *Try:
		d.open("try")
		d.child(x.X)
	case *Move:
		d.open("move")
		d.word(x.Name)
	case *ClosureExpr:
		d.open("closure")
		list(d, x.Params)
		d.child(x.Expr)
		d.child(x.Body)
	case *Copy:
		d.open("copy")
		d.child(x.X)
	case *Take:
		d.open("take")
		d.child(x.Place)
	case *Replace:
		d.open("replace")
		d.child(x.Place)
		d.child(x.Value)
	case *Drop:
		d.open("drop")
		d.word(x.Name)
	case *TrapExpr:
		d.open("trap")
	case *ConvExpr:
		d.open("conv")
		d.word(x.To)
		d.child(x.X)
		d.flag(x.Truncate)
	case *FieldInit:
		d.open("finit")
		d.word(x.Name)
		d.child(x.Value)
	case *StructLit:
		d.open("structlit")
		d.child(x.Type)
		list(d, x.Fields)
	case *ArrayLit:
		d.open("arraylit")
		list(d, x.Elems)
	case *RangeExpr:
		d.open("range")
		d.child(x.Lo)
		d.child(x.Hi)
	case *UnitLit:
		d.open("unitlit")
	case *Paren:
		d.open("paren")
		d.child(x.X)
	default:
		panic(fmt.Sprintf("ast.Dump: no form for %T", n))
	}
	d.close()
}
