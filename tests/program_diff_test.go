package tests

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// TestProgramDifferential is the whole-language counterpart of the proof
// and integer differential tests: the interpreter is the behavioural oracle
// of the native backend (README), so random programs drawn from every
// feature the two engines implement separately (structs, unions and match,
// owned values consumed by with-bindings, Vec and Box operations, with-bindings,
// closures with view and edit captures, byte-order reads and writes,
// integer conversions and divisions that may trap, generic functions, and
// the std/str and std/map library code) must print the same output, exit
// with the same status and, when they trap, report the same record, in
// the interpreter and in both native builds. KERN_DIFF_PROGRAMS,
// KERN_DIFF_SEED and KERN_DIFF_DUMP apply as for the other two.
func TestProgramDifferential(t *testing.T) {
	if !native.Available() {
		t.Skip("clang not available")
	}
	n := 24
	if s := os.Getenv("KERN_DIFF_PROGRAMS"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			t.Fatalf("KERN_DIFF_PROGRAMS=%q", s)
		}
		n = v
	}
	seed := int64(1)
	if s := os.Getenv("KERN_DIFF_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("KERN_DIFF_SEED=%q", s)
		}
		seed = v
	}
	for i := range n {
		s := seed + int64(i)
		t.Run(fmt.Sprintf("seed%d", s), func(t *testing.T) {
			t.Parallel()
			src := genMixed(rand.New(rand.NewSource(s)))
			if os.Getenv("KERN_DIFF_DUMP") != "" {
				t.Log(src)
			}
			f := &source.File{Name: "mixed.kern", Bytes: []byte(src)}
			files := []*source.File{f}
			prog := resolve.Resolve(files)
			if len(prog.Diags) > 0 {
				t.Fatalf("generated program does not resolve: %s\n%s", prog.Diags[0].Message, src)
			}
			if ds := check.Check(prog); len(ds) > 0 {
				t.Fatalf("generated program does not check: %s\n%s", ds[0].Message, src)
			}
			mod, ds := lower.Lower(prog)
			if len(ds) > 0 {
				t.Fatalf("lower: %s\n%s", ds[0].Message, src)
			}
			if err := opt.Run(mod, opt.Options{}); err != nil {
				t.Fatalf("validate: %v\n%s", err, src)
			}
			var out, errb bytes.Buffer
			it := interp.New(mod, "mixed.kern", &out, &errb)
			status := it.Run()
			if status != 0 && status != 134 {
				t.Fatalf("interpreter status %d, stderr %q\n%s", status, errb.String(), src)
			}
			for _, level := range []string{"debug", "release"} {
				t.Run(level, func(t *testing.T) {
					defer func() {
						if t.Failed() {
							t.Logf("program:\n%s", src)
						}
					}()
					nativeRunOpt(t, files, header{}, out.Bytes(), errb.Bytes(), status, level)
				})
			}
		})
	}
}

// genMixed writes one random program: a fixed set of helpers and types,
// then a random sequence of self-contained blocks, each printing what it
// computed. About one block in ten takes a path that traps (a division by
// zero, a conversion out of range, an index past the end), so most programs
// run to completion and the rest compare their trap records.
func genMixed(r *rand.Rand) string {
	var b strings.Builder
	b.WriteString(`module d
import "std/str"
import "std/map"
struct Pt { x: u64; y: u64 }
struct Bag { items: Vec[u64]; tag: u64 }
union Shape { Dot; Line(u64); Rect(w: u64, h: u64) }
func area(s: view[Shape]) -> u64 {
    match s {
        Dot => return 1u64
        Line(n) => return n
        Rect(w, h) => return wrapping_mul(w, h)
    }
}
func apply(f: closure(u64) -> u64, x: u64) -> u64 { return f(x) }
func each(n: u64, f: closure(u64)) {
    i := 0u64
    while i < n { f(i); i = i + 1u64 }
}
func fold(words: view[]u64, f: closure(u64, u64) -> u64) -> u64 {
    acc := 0u64
    for w in words { acc = f(acc, w) }
    return acc
}
func dup[T: Copy](x: T) -> T { return x }
func total(bag: view[Bag]) -> u64 {
    s := bag.tag
    for x in bag.items.as_view() { s = wrapping_add(s, x) }
    return s
}
func take_box(b: own Option[Box[u64]]) -> u64 {
    with o := move b {
        match o {
            Some(bx) => { with v := bx.get() { return copy(v) } }
            None => return 0u64
        }
    }
}
func main() -> i32 {
`)
	blocks := 3 + r.Intn(7)
	for range blocks {
		b.WriteString("    {\n")
		switch r.Intn(14) {
		case 0:
			// struct arithmetic and a generic identity
			fmt.Fprintf(&b, "        p := Pt { x: %du64, y: %du64 }\n        q := dup[u64](wrapping_add(p.x, p.y))\n        print_u64(wrapping_mul(q, %du64))\n", r.Intn(1000), r.Intn(1000), 1+r.Intn(9))
		case 1:
			// a union through a view match
			shapes := []string{"Dot", fmt.Sprintf("Line(%du64)", r.Intn(50)), fmt.Sprintf("Rect(%du64, %du64)", r.Intn(20), r.Intn(20))}
			fmt.Fprintf(&b, "        s := Shape.%s\n        print_u64(area(s))\n", shapes[r.Intn(3)])
		case 2:
			// an owned Option[Box] consumed by a with-binding
			if r.Intn(2) == 0 {
				fmt.Fprintf(&b, "        print_u64(take_box(Some(Box[u64].new(%du64))))\n", r.Intn(500))
			} else {
				b.WriteString("        print_u64(take_box(None))\n")
			}
		case 3:
			// a vector: pushes, a pop, an edit through at, a clone, the sum
			n := r.Intn(6)
			b.WriteString("        v := Vec[u64].new()\n")
			for range n {
				fmt.Fprintf(&b, "        v.push(%du64)\n", r.Intn(100))
			}
			b.WriteString("        match v.pop() {\n            Some(x) => { print_u64(x) }\n            None => { print_str(\"empty\") }\n        }\n        println()\n")
			if n > 1 {
				fmt.Fprintf(&b, "        with e := v.at(%dusize) { e = wrapping_add(e, %du64) }\n", r.Intn(n-1), r.Intn(10))
			}
			b.WriteString("        w := v.clone()\n        w.push(1u64)\n        bag := Bag { items: move w, tag: u64(v.len()) }\n        print_u64(total(bag))\n")
		case 4:
			// closures: a view capture, an edit capture in a loop
			fmt.Fprintf(&b, "        k := %du64\n        print_u64(apply(|x| wrapping_add(x, k), %du64))\n        println()\n        acc := 0u64\n        each(%du64, |i| { acc = wrapping_add(acc, wrapping_mul(i, k)) })\n        print_u64(acc)\n", r.Intn(100), r.Intn(100), r.Intn(6))
		case 5:
			// a fold over an array view with a closure
			fmt.Fprintf(&b, "        var a: [4]u64 = zero()\n        a[1usize] = %du64\n        a[3usize] = %du64\n        with w := a[0usize : 4usize] { print_u64(fold(w, |s, x| wrapping_add(s, wrapping_mul(x, %du64)))) }\n", r.Intn(100), r.Intn(100), 1+r.Intn(5))
		case 6:
			// byte-order writes and reads
			fmt.Fprintf(&b, "        var y: [8]u8 = zero()\n        store_be[u32](y, %dusize, %du32)\n        store_le[u16](y, %dusize, %du16)\n        print_u64(load_le[u64](y, 0usize))\n        println()\n        print_u64(u64(load_be[u16](y, %dusize)))\n", r.Intn(5), r.Intn(1<<20), 4+r.Intn(3), r.Intn(1<<16), r.Intn(7))
		case 7:
			// a division that may trap, and a conversion that may trap
			d := r.Intn(9)
			fmt.Fprintf(&b, "        n := %du64\n        d := %du64\n        print_u64(n / d)\n        println()\n        print_u64(u64(u8(n %% %du64)))\n", r.Intn(5000), d, 200+r.Intn(100))
		case 8:
			// a sum loop with checked and saturating forms
			fmt.Fprintf(&b, "        s := %du64\n        i := 0u64\n        while i < %du64 {\n            match checked_mul(s, 3u64) {\n                Some(m) => s = m\n                None => s = saturating_add(s, i)\n            }\n            i = i + 1u64\n        }\n        print_u64(s)\n", r.Intn(1<<40), r.Intn(30))
		case 9:
			// std/str
			fmt.Fprintf(&b, "        t := str.String.from(\"n=\")\n        t.push_u64(%du64)\n        t.push_str(\" end\")\n        print_str(t.as_view())\n        println()\n        print_u64(str.hash(t.as_view()) %% 1000u64)\n        println()\n        match str.find(t.as_view(), \"end\") {\n            Some(i) => print_u64(u64(i))\n            None => print_str(\"none\")\n        }\n", r.Intn(1<<30))
		case 10:
			// std/map
			n := 1 + r.Intn(40)
			fmt.Fprintf(&b, "        m := map.Map[u64, u64].new()\n        j := 0u64\n        while j < %du64 { m.insert(wrapping_mul(j, %du64), j); j = j + 1u64 }\n        print_u64(u64(m.len()))\n        println()\n        print_bool(m.contains(%du64))\n        println()\n        match m.remove(%du64) {\n            Some(old) => print_u64(old)\n            None => print_str(\"none\")\n        }\n", n, 1+r.Intn(7), r.Intn(60), r.Intn(60))
		case 11:
			// an index into a vector that may be past the end
			n := 1 + r.Intn(5)
			b.WriteString("        v := Vec[u64].new()\n")
			for range n {
				fmt.Fprintf(&b, "        v.push(%du64)\n", r.Intn(100))
			}
			fmt.Fprintf(&b, "        print_u64(v.get(%dusize))\n", r.Intn(n+1))
		case 12:
			// a Box edited in place through a with-binding
			fmt.Fprintf(&b, "        bx := Box[u64].new(%du64)\n        with v := bx.at() { v = wrapping_mul(v, %du64) }\n        with v := bx.get() { print_u64(copy(v)) }\n", r.Intn(1000), r.Intn(10))
		case 13:
			// a nested closure capturing the outer closure's parameter
			fmt.Fprintf(&b, "        k := %du64\n        print_u64(apply(|x| apply(|y| wrapping_add(wrapping_add(x, y), k), x), %du64))\n", r.Intn(100), r.Intn(100))
		}
		b.WriteString("        println()\n    }\n")
	}
	b.WriteString("    return 0i32\n}\n")
	return b.String()
}
