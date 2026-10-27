// Package ir is kern's typed, ownership-aware semantic IR (spec §7). It is
// monomorphic: generic functions are specialized before lowering. Every
// instruction that the spec requires to be explicit (move, drop,
// access_begin/access_end, check_*, unsafe_begin/unsafe_end) is a distinct
// instruction, and every check carries a provenance tag.
package ir

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/resolve"
)

// Module is a lowered program.
type Module struct {
	Name    string
	Globals []*Global
	Funcs   []*Func
	Entry   string // name of the entry function ("main") or ""
	byName  map[string]*Func
}

// Func looks up a function by name.
func (m *Module) Func(name string) *Func {
	if m.byName == nil {
		m.byName = map[string]*Func{}
		for _, f := range m.Funcs {
			m.byName[f.Name] = f
		}
	}
	return m.byName[name]
}

// Add appends a function.
func (m *Module) Add(f *Func) {
	m.Funcs = append(m.Funcs, f)
	if m.byName != nil {
		m.byName[f.Name] = f
	}
}

// Func is one lowered (specialized) function.
type Func struct {
	Name      string
	Params    []*Local // in order; access parameters have Conv view/edit
	Rets      []resolve.Type
	Locals    []*Local // all slots, params first
	Blocks    []*Block
	Release   string // release function name for accessor functions
	Intrinsic bool   // provided by the runtime (std prelude without body)
	// Library marks a std/core function with a body: a trap inside it is
	// attributed to the call that entered the library (the span its
	// callers pass along), as the runtime's C containers reported it.
	Library bool
	// Inlined marks a function every call of which was inlined (opt.Inline)
	// and that nothing references any more: not emitted, run or reported.
	Inlined   bool
	Extern    string // C ABI symbol when an extern declaration
	Contract  *ast.Contract
	Pos       ast.Pos
	File      string // declaring file (machine-API locations)
	Entry     *Block
	NumValues int
}

// Local is an owned slot (parameter, local variable, or compiler temporary)
// or an access parameter slot.
type Local struct {
	ID   int
	Name string
	Type resolve.Type
	Conv ast.Conv // ConvValue (owned), ConvOwn, ConvView, ConvEdit, ConvOut
	Temp bool
}

// Value is an SSA temporary identified by index.
type Value struct {
	ID   int
	Type resolve.Type
}

// Block is a basic block ending in a terminator.
type Block struct {
	ID     int
	Name   string
	Instrs []Instr
	Term   Term
}

// Provenance tags a check (spec §7 invariant 3).
type Provenance struct {
	Label    string // "runtime-checked" or "proven"
	ProvenBy string // "loop_bound" | "dominated_by_check" | "constant" | ""
}

// RuntimeChecked is the provenance of every check emitted by the lowering.
var RuntimeChecked = Provenance{Label: "runtime-checked"}

// Place denotes a sub-place of a local or of an access value.
type Place struct {
	Local  *Local  // root local, or nil when Access or Global is set
	Access *Value  // root access value (a projection), or nil
	Global *Global // root thread_local global, or nil
	Path   []PathElem
}

// Global is a thread_local module variable (spec §6 TLS): a Copy type with
// a constant initializer (an integer, a float, a bool, or nil for zero).
type Global struct {
	ID   int
	Name string
	Type resolve.Type
	Init any
}

// PathKind classifies a path element.
type PathKind int

// Path element kinds.
const (
	PathField   PathKind = iota // struct field by index
	PathIndex                   // array/sequence element by value
	PathVariant                 // union payload field (Tag, Field)
	PathSlice                   // sub-slice [Lo, Hi) by values (nil = open)
)

// PathElem is one step of a place path.
type PathElem struct {
	Kind  PathKind
	Field int    // field index (PathField) or payload index (PathVariant)
	Name  string // field name, for printing
	Tag   int    // variant tag (PathVariant)
	Index *Value // PathIndex
	Lo    *Value // PathSlice
	Hi    *Value // PathSlice
}

// Instr is one IR instruction.
type Instr interface{ instr() }

// Const materializes a constant.
type Const struct {
	Dst   *Value
	Int   uint64
	Neg   bool
	Float float64
	Bool  bool
	Bytes []byte // string literal (view[]u8)
	Unit  bool
}

// Load reads a Copy value from a place.
type Load struct {
	Dst   *Value
	Place Place
	Pos   ast.Pos // the source expression, for show-copies
}

// Store writes a value into a place; the previous contents are not destroyed
// (an explicit Drop precedes a replacing store).
type Store struct {
	Place Place
	Src   *Value
}

// Move moves the value out of a place into a temporary. For a whole local
// the local becomes moved (spec §5.1); for a sub-place (take/replace, match
// on an owned subject) the lowering immediately refills or destroys it.
type Move struct {
	Dst *Value
	Src Place
}

// Drop destroys the value at a place (spec §5.7); static (no drop flags).
// Type is the type of the destroyed value, which directs destruction.
type Drop struct {
	Place Place
	Type  resolve.Type
	Pos   ast.Pos
}

// AccessBegin opens an access of the given kind on a place (spec §5.4).
type AccessBegin struct {
	Dst   *Value
	Edit  bool
	Place Place
	Pos   ast.Pos
}

// AccessEnd closes an access opened by AccessBegin or returned by a call.
type AccessEnd struct {
	Access *Value
	// Release names a release function to call with the receiver before the
	// access ends (accessor release, spec §5.3), or "".
	Release string
	Recv    *Value
}

// Call invokes a function by name or through a function value.
type Call struct {
	Dsts      []*Value
	Callee    string // function name when Fn == nil
	Fn        *Value // function value for indirect calls
	Args      []*Value
	Intrinsic string // intrinsic name when the callee is runtime-provided
	Pos       ast.Pos
	// Release marks a call to an accessor with a release function; the
	// returned access's AccessEnd must call it.
	Release string
	// TypeArgs are the callee's type arguments (owner parameters first,
	// then the function's own), for runtime-provided generic intrinsics.
	TypeArgs []resolve.Type
}

// Binary is an arithmetic, bitwise or comparison operation with wrapping
// integer semantics; a preceding CheckOverflow traps where §4.1 requires.
type Binary struct {
	Dst  *Value
	Op   string
	L, R *Value
	// Mode is "" (checked by a preceding CheckOverflow), "wrapping",
	// "saturating" or "checked" (yields Option).
	Mode string
}

// Unary is negation or logical not.
type Unary struct {
	Dst *Value
	Op  string
	X   *Value
}

// Conv converts between primitive types (spec §4.1); Truncate selects the
// non-trapping form.
type Conv struct {
	Dst      *Value
	To       string
	X        *Value
	Truncate bool
	Saturate bool
}

// CheckOverflow traps with kind overflow/div_zero/shift when the operation
// would overflow (spec §4.1).
type CheckOverflow struct {
	Op   string
	L, R *Value
	Type string
	Prov Provenance
	Pos  ast.Pos
}

// CheckBounds traps with kind bounds unless Index < Len (or Lo <= Hi <= Len).
type CheckBounds struct {
	Index  *Value
	Lo, Hi *Value
	Len    *Value
	Prov   Provenance
	Pos    ast.Pos
}

// CheckConv traps with kind conversion unless X fits the target type.
type CheckConv struct {
	X    *Value
	To   string
	Prov Provenance
	Pos  ast.Pos
}

// Len yields the length of an array place or sequence access.
type Len struct {
	Dst   *Value
	Place Place
}

// MakeStruct builds a struct value from field values in declaration order.
type MakeStruct struct {
	Dst    *Value
	Fields []*Value
}

// MakeUnion builds a union value.
type MakeUnion struct {
	Dst    *Value
	Tag    int
	Fields []*Value
}

// MakeArray builds an array from element values.
type MakeArray struct {
	Dst   *Value
	Elems []*Value
}

// Zero materializes the all-zero value of a zeroable type.
type Zero struct{ Dst *Value }

// Tag reads a union's discriminant.
type Tag struct {
	Dst   *Value
	Place Place
}

// FuncRef materializes a function value.
type FuncRef struct {
	Dst  *Value
	Name string
}

// Trap terminates with the given kind (spec §6).
type Trap struct {
	Kind string
	Pos  ast.Pos
}

// UnsafeBegin opens an unsafe region.
type UnsafeBegin struct{}

// UnsafeEnd closes an unsafe region.
type UnsafeEnd struct{}

// Copy duplicates a value (bitwise for Copy types).
type Copy struct {
	Dst *Value
	Src *Value
}

// Init marks an out-storage local or field as initialized by a store (no
// runtime effect; consumed by the validator).
type Init struct{ Place Place }

func (*Const) instr()         {}
func (*Load) instr()          {}
func (*Store) instr()         {}
func (*Move) instr()          {}
func (*Drop) instr()          {}
func (*AccessBegin) instr()   {}
func (*AccessEnd) instr()     {}
func (*Call) instr()          {}
func (*Binary) instr()        {}
func (*Unary) instr()         {}
func (*Conv) instr()          {}
func (*CheckOverflow) instr() {}
func (*CheckBounds) instr()   {}
func (*CheckConv) instr()     {}
func (*Len) instr()           {}
func (*MakeStruct) instr()    {}
func (*MakeUnion) instr()     {}
func (*MakeArray) instr()     {}
func (*Zero) instr()          {}
func (*Tag) instr()           {}
func (*FuncRef) instr()       {}
func (*Trap) instr()          {}
func (*UnsafeBegin) instr()   {}
func (*UnsafeEnd) instr()     {}
func (*Copy) instr()          {}
func (*Init) instr()          {}

// Term is a block terminator.
type Term interface{ term() }

// Br jumps unconditionally.
type Br struct{ Target *Block }

// CondBr branches on a bool value.
type CondBr struct {
	Cond       *Value
	Then, Else *Block
}

// Switch branches on a union tag.
type Switch struct {
	Tag     *Value
	Cases   map[int]*Block
	Default *Block
}

// Ret returns values.
type Ret struct{ Vals []*Value }

// Unreachable marks a point that control cannot reach (after a trap).
type Unreachable struct{}

func (*Br) term()          {}
func (*CondBr) term()      {}
func (*Switch) term()      {}
func (*Ret) term()         {}
func (*Unreachable) term() {}
