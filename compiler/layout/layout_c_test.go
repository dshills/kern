package layout_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// TestLayoutMatchesC compiles the §4.1 layout of struct and union fixtures
// against offsetof/sizeof of equivalent C declarations (PLAN.md 2.1): a
// struct is the same C struct; a union is a C struct of the discriminant
// followed by a C union of the variant payload structs.
func TestLayoutMatchesC(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not available")
	}
	src := `module lay
struct P { a: u8; b: u64; c: u16 }
struct Q { p: P; f: f32; g: [3]u8; h: bool }
struct R { x: i32; q: Q; y: u8 }
union U { A(u8); B(u64, u8); C(P, i16); D }
union V { X; Y(Q) }
struct W { u: U; v: V; z: u8 }
`
	f := &source.File{Name: "lay.kern", Bytes: []byte(src)}
	prog := resolve.Resolve([]*source.File{f})
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	m := prog.Main
	order := []string{"P", "Q", "R", "U", "V", "W"}
	var c strings.Builder
	c.WriteString("#include <stddef.h>\n#include <stdint.h>\n#include <stdbool.h>\n#include <stdio.h>\n")
	for _, name := range order {
		td := m.Types[name]
		n := &resolve.Named{Decl: td}
		switch td.Kind {
		case resolve.KindStruct:
			fmt.Fprintf(&c, "typedef struct { ")
			for _, fd := range td.Fields {
				c.WriteString(cDecl(fd.Type, fd.Name) + "; ")
			}
			fmt.Fprintf(&c, "} %s;\n", name)
		case resolve.KindUnion:
			li := layout.Of(n)
			tag := "uint8_t"
			if li.TagSize == 2 {
				tag = "uint16_t"
			}
			fmt.Fprintf(&c, "typedef struct { %s tag; union { ", tag)
			for _, v := range td.Variants {
				if len(v.Fields) == 0 {
					continue
				}
				fmt.Fprintf(&c, "struct { ")
				for i, fd := range v.Fields {
					c.WriteString(cDecl(fd.Type, fmt.Sprintf("f%d", i)) + "; ")
				}
				fmt.Fprintf(&c, "} %s; ", v.Name)
			}
			fmt.Fprintf(&c, "char pad_; } u; } %s;\n", name)
		}
	}
	c.WriteString("int main(void) {\n")
	type want struct {
		key string
		val int
	}
	var wants []want
	for _, name := range order {
		td := m.Types[name]
		n := &resolve.Named{Decl: td}
		li := layout.Of(n)
		fmt.Fprintf(&c, "printf(\"%s.size=%%zu\\n\", sizeof(%s));\n", name, name)
		fmt.Fprintf(&c, "printf(\"%s.align=%%zu\\n\", _Alignof(%s));\n", name, name)
		wants = append(wants, want{name + ".size", li.Size}, want{name + ".align", li.Align})
		switch td.Kind {
		case resolve.KindStruct:
			for i, fd := range td.Fields {
				fmt.Fprintf(&c, "printf(\"%s.%s=%%zu\\n\", offsetof(%s, %s));\n", name, fd.Name, name, fd.Name)
				wants = append(wants, want{name + "." + fd.Name, li.Fields[i]})
			}
		case resolve.KindUnion:
			for _, v := range td.Variants {
				if len(v.Fields) == 0 {
					continue
				}
				offs := layout.VariantLayout(n, v)
				for i := range v.Fields {
					fmt.Fprintf(&c, "printf(\"%s.%s.f%d=%%zu\\n\", offsetof(%s, u.%s.f%d));\n", name, v.Name, i, name, v.Name, i)
					wants = append(wants, want{fmt.Sprintf("%s.%s.f%d", name, v.Name, i), offs[i]})
				}
			}
		}
	}
	c.WriteString("return 0; }\n")
	dir := t.TempDir()
	cf := filepath.Join(dir, "lay.c")
	exe := filepath.Join(dir, "lay")
	if err := os.WriteFile(cf, []byte(c.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(clang, "-std=c11", "-o", exe, cf).CombinedOutput(); err != nil {
		t.Fatalf("clang: %v\n%s\n%s", err, out, c.String())
	}
	out, err := exec.Command(exe).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		got[k] = v
	}
	for _, w := range wants {
		if got[w.key] != fmt.Sprint(w.val) {
			t.Errorf("%s: kern layout %d, C %s", w.key, w.val, got[w.key])
		}
	}
}

func cDecl(t resolve.Type, name string) string {
	switch x := t.(type) {
	case *resolve.Prim:
		m := map[string]string{"u8": "uint8_t", "u16": "uint16_t", "u32": "uint32_t", "u64": "uint64_t", "usize": "size_t", "i8": "int8_t", "i16": "int16_t", "i32": "int32_t", "i64": "int64_t", "isize": "ptrdiff_t", "f32": "float", "f64": "double", "bool": "bool"}
		return m[x.Name] + " " + name
	case *resolve.Array:
		return cDecl(x.Elem, name+fmt.Sprintf("[%d]", x.Len))
	case *resolve.Named:
		return x.Decl.Name + " " + name
	}
	return "void *" + name
}
