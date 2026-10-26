package ir

import (
	"fmt"
	"sort"
	"strings"
)

// Print renders a module in the textual IR form used by `emit-ir`.
func Print(m *Module) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "module %s\n", m.Name)
	if m.Entry != "" {
		fmt.Fprintf(&sb, "entry %s\n", m.Entry)
	}
	for _, f := range m.Funcs {
		sb.WriteString("\n")
		PrintFunc(&sb, f)
	}
	return sb.String()
}

// PrintFunc renders one function.
func PrintFunc(sb *strings.Builder, f *Func) {
	var ps []string
	for _, p := range f.Params {
		ps = append(ps, fmt.Sprintf("%s %s: %s", convName(p), p.Name, p.Type))
	}
	var rs []string
	for _, r := range f.Rets {
		rs = append(rs, r.String())
	}
	kw := "func"
	if f.Intrinsic {
		kw = "intrinsic"
	}
	if f.Extern != "" {
		kw = "extern " + f.Extern
	}
	fmt.Fprintf(sb, "%s %s(%s)", kw, f.Name, strings.Join(ps, ", "))
	if len(rs) > 0 {
		fmt.Fprintf(sb, " -> (%s)", strings.Join(rs, ", "))
	}
	if f.Release != "" {
		fmt.Fprintf(sb, " release %s", f.Release)
	}
	if f.Intrinsic || f.Extern != "" {
		sb.WriteString("\n")
		return
	}
	sb.WriteString(" {\n")
	for _, l := range f.Locals {
		if l.Conv == 0 || l.Temp {
			fmt.Fprintf(sb, "  local %s: %s\n", localName(l), l.Type)
		}
	}
	for _, b := range f.Blocks {
		fmt.Fprintf(sb, "%s:\n", blockName(b))
		for _, in := range b.Instrs {
			fmt.Fprintf(sb, "  %s\n", printInstr(in))
		}
		fmt.Fprintf(sb, "  %s\n", printTerm(b.Term))
	}
	sb.WriteString("}\n")
}

func convName(l *Local) string {
	switch l.Conv {
	case 1:
		return "view"
	case 2:
		return "edit"
	case 3:
		return "own"
	case 4:
		return "out"
	}
	return "val"
}

func localName(l *Local) string {
	if l.Temp {
		return fmt.Sprintf("%%l%d", l.ID)
	}
	return "%" + l.Name
}

func blockName(b *Block) string {
	if b.Name != "" {
		return fmt.Sprintf("bb%d.%s", b.ID, b.Name)
	}
	return fmt.Sprintf("bb%d", b.ID)
}

func v(x *Value) string {
	if x == nil {
		return "_"
	}
	return fmt.Sprintf("%%v%d", x.ID)
}

func vs(xs []*Value) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = v(x)
	}
	return strings.Join(parts, ", ")
}

// PlaceString renders a place.
func PlaceString(p Place) string {
	var sb strings.Builder
	switch {
	case p.Local != nil:
		sb.WriteString(localName(p.Local))
	case p.Global != nil:
		sb.WriteString("@" + p.Global.Name)
	default:
		sb.WriteString("*" + v(p.Access))
	}
	for _, e := range p.Path {
		switch e.Kind {
		case PathField:
			fmt.Fprintf(&sb, ".%s", e.Name)
		case PathIndex:
			fmt.Fprintf(&sb, "[%s]", v(e.Index))
		case PathVariant:
			fmt.Fprintf(&sb, ".<%d>.%d", e.Tag, e.Field)
		case PathSlice:
			fmt.Fprintf(&sb, "[%s:%s]", v(e.Lo), v(e.Hi))
		}
	}
	return sb.String()
}

func prov(p Provenance) string {
	if p.ProvenBy != "" {
		return fmt.Sprintf(" @proven(%s)", p.ProvenBy)
	}
	return " @" + p.Label
}

func printInstr(in Instr) string {
	switch i := in.(type) {
	case *Const:
		switch {
		case i.Unit:
			return fmt.Sprintf("%s = const () ", v(i.Dst))
		case i.Bytes != nil:
			return fmt.Sprintf("%s = const %s %q", v(i.Dst), i.Dst.Type, string(i.Bytes))
		case i.Dst.Type.String() == "bool":
			return fmt.Sprintf("%s = const bool %v", v(i.Dst), i.Bool)
		case strings.HasPrefix(i.Dst.Type.String(), "f"):
			return fmt.Sprintf("%s = const %s %g", v(i.Dst), i.Dst.Type, i.Float)
		}
		s := fmt.Sprintf("%d", i.Int)
		if i.Neg {
			s = "-" + s
		}
		return fmt.Sprintf("%s = const %s %s", v(i.Dst), i.Dst.Type, s)
	case *Load:
		return fmt.Sprintf("%s = load %s", v(i.Dst), PlaceString(i.Place))
	case *Store:
		return fmt.Sprintf("store %s, %s", PlaceString(i.Place), v(i.Src))
	case *Move:
		return fmt.Sprintf("%s = move %s", v(i.Dst), PlaceString(i.Src))
	case *Drop:
		return fmt.Sprintf("drop %s", PlaceString(i.Place))
	case *AccessBegin:
		k := "view"
		if i.Edit {
			k = "edit"
		}
		return fmt.Sprintf("%s = access_begin %s %s", v(i.Dst), k, PlaceString(i.Place))
	case *AccessEnd:
		if i.Release != "" {
			return fmt.Sprintf("access_end %s release %s(%s)", v(i.Access), i.Release, v(i.Recv))
		}
		return fmt.Sprintf("access_end %s", v(i.Access))
	case *Call:
		callee := i.Callee
		if i.Fn != nil {
			callee = "*" + v(i.Fn)
		}
		if i.Intrinsic != "" {
			callee = "intrinsic " + i.Intrinsic
		}
		s := fmt.Sprintf("call %s(%s)", callee, vs(i.Args))
		if len(i.Dsts) > 0 {
			s = vs(i.Dsts) + " = " + s
		}
		if i.Release != "" {
			s += " release " + i.Release
		}
		return s
	case *Binary:
		mode := ""
		if i.Mode != "" {
			mode = "." + i.Mode
		}
		return fmt.Sprintf("%s = %s%s %s, %s", v(i.Dst), opName(i.Op), mode, v(i.L), v(i.R))
	case *Unary:
		return fmt.Sprintf("%s = %s %s", v(i.Dst), opName(i.Op), v(i.X))
	case *Conv:
		k := "conv"
		if i.Truncate {
			k = "truncate"
		}
		if i.Saturate {
			k = "saturate"
		}
		return fmt.Sprintf("%s = %s %s, %s", v(i.Dst), k, i.To, v(i.X))
	case *CheckOverflow:
		return fmt.Sprintf("check_overflow %s %s, %s : %s%s", opName(i.Op), v(i.L), v(i.R), i.Type, prov(i.Prov))
	case *CheckBounds:
		if i.Index != nil {
			return fmt.Sprintf("check_bounds %s < %s%s", v(i.Index), v(i.Len), prov(i.Prov))
		}
		return fmt.Sprintf("check_bounds %s..%s <= %s%s", v(i.Lo), v(i.Hi), v(i.Len), prov(i.Prov))
	case *CheckConv:
		return fmt.Sprintf("check_conv %s : %s%s", v(i.X), i.To, prov(i.Prov))
	case *Len:
		return fmt.Sprintf("%s = len %s", v(i.Dst), PlaceString(i.Place))
	case *MakeStruct:
		return fmt.Sprintf("%s = struct %s {%s}", v(i.Dst), i.Dst.Type, vs(i.Fields))
	case *MakeUnion:
		return fmt.Sprintf("%s = union %s <%d> {%s}", v(i.Dst), i.Dst.Type, i.Tag, vs(i.Fields))
	case *MakeArray:
		return fmt.Sprintf("%s = array %s {%s}", v(i.Dst), i.Dst.Type, vs(i.Elems))
	case *Zero:
		return fmt.Sprintf("%s = zero %s", v(i.Dst), i.Dst.Type)
	case *Tag:
		return fmt.Sprintf("%s = tag %s", v(i.Dst), PlaceString(i.Place))
	case *FuncRef:
		return fmt.Sprintf("%s = funcref %s", v(i.Dst), i.Name)
	case *Trap:
		return fmt.Sprintf("trap %s", i.Kind)
	case *UnsafeBegin:
		return "unsafe_begin"
	case *UnsafeEnd:
		return "unsafe_end"
	case *Copy:
		return fmt.Sprintf("%s = copy %s", v(i.Dst), v(i.Src))
	case *Init:
		return fmt.Sprintf("init %s", PlaceString(i.Place))
	}
	return fmt.Sprintf("?%T", in)
}

var opNames = map[string]string{
	"+": "add", "-": "sub", "*": "mul", "/": "div", "%": "rem", "&": "and", "|": "or", "^": "xor",
	"<<": "shl", ">>": "shr", "==": "eq", "!=": "ne", "<": "lt", "<=": "le", ">": "gt", ">=": "ge",
	"&&": "land", "||": "lor", "!": "not", "neg": "neg",
}

func opName(op string) string {
	if n, ok := opNames[op]; ok {
		return n
	}
	return op
}

func printTerm(t Term) string {
	switch x := t.(type) {
	case *Br:
		return "br " + blockName(x.Target)
	case *CondBr:
		return fmt.Sprintf("condbr %s, %s, %s", v(x.Cond), blockName(x.Then), blockName(x.Else))
	case *Switch:
		keys := make([]int, 0, len(x.Cases))
		for k := range x.Cases {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%d: %s", k, blockName(x.Cases[k])))
		}
		d := "none"
		if x.Default != nil {
			d = blockName(x.Default)
		}
		return fmt.Sprintf("switch %s [%s] default %s", v(x.Tag), strings.Join(parts, ", "), d)
	case *Ret:
		return "ret " + vs(x.Vals)
	case *Unreachable:
		return "unreachable"
	case nil:
		return "<no terminator>"
	}
	return fmt.Sprintf("?%T", t)
}
