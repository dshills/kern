package tests

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// TestUTF8Differential checks std/str's utf8_valid and utf8_count against
// Go's unicode/utf8 (specs/std-gaps §4 item 6), which follows the same
// Unicode Table 3-7: one kern program over 600 byte strings from a fixed
// seed, two thirds near-valid sequences of lead and continuation bytes,
// a third well-formed strings of random scalar values, run in the
// interpreter and compared line by line.
func TestUTF8Differential(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	var cases [][]byte
	// near-valid strings: chunks of a lead byte (every lead from 0xC0 up,
	// the special ones E0, ED, F0 and F4 included) followed by one fewer,
	// as many or one more continuation bytes than it announces, each drawn
	// from the whole 0x80-0xBF range, so overlong forms, surrogates, values
	// above U+10FFFF, cut-off and over-long sequences all occur; with
	// ASCII and arbitrary bytes between the chunks
	leads := []byte{0xC0, 0xC1, 0xC2, 0xDF, 0xE0, 0xE1, 0xEC, 0xED, 0xEE, 0xEF, 0xF0, 0xF1, 0xF3, 0xF4, 0xF5, 0xF8, 0xFF}
	announce := func(lead byte) int {
		switch {
		case lead >= 0xF0:
			return 3
		case lead >= 0xE0:
			return 2
		}
		return 1
	}
	for len(cases) < 400 {
		var b []byte
		for k := 1 + r.Intn(3); k > 0; k-- {
			switch c := r.Intn(10); {
			case c < 2:
				b = append(b, byte(0x20+r.Intn(0x5F)))
			case c < 3:
				b = append(b, byte(r.Intn(256)))
			default:
				lead := leads[r.Intn(len(leads))]
				b = append(b, lead)
				for n := announce(lead) + r.Intn(3) - 1; n > 0; n-- {
					b = append(b, byte(0x80+r.Intn(0x40)))
				}
			}
		}
		cases = append(cases, b)
	}
	edges := []rune{0, 0x7F, 0x80, 0x7FF, 0x800, 0xD7FF, 0xE000, 0xFFFF, 0x10000, 0x10FFFF}
	for len(cases) < 600 {
		var b []byte
		for k := r.Intn(5); k >= 0; k-- {
			var c rune
			switch r.Intn(3) {
			case 0:
				c = edges[r.Intn(len(edges))]
			case 1:
				c = rune(r.Intn(0x80))
			default:
				c = rune(r.Intn(0x110000))
				if c >= 0xD800 && c <= 0xDFFF {
					c = 0xFFFD
				}
			}
			b = utf8.AppendRune(b, c)
		}
		cases = append(cases, b)
	}
	var src, want strings.Builder
	src.WriteString("module t\nimport \"std/str\"\nfunc report(s: view[]u8) {\n    if str.utf8_valid(s) { print_str(\"valid \") } else { print_str(\"invalid \") }\n    match str.utf8_count(s) {\n        Some(n) => print_u64(u64(n))\n        None => print_str(\"none\")\n    }\n    println()\n}\nfunc main() -> i32 {\n    s := str.String.new()\n")
	valid, invalid := 0, 0
	for _, b := range cases {
		src.WriteString("    s.clear()\n")
		for _, x := range b {
			fmt.Fprintf(&src, "    s.push(%du8)\n", x)
		}
		src.WriteString("    report(s.as_view())\n")
		if utf8.Valid(b) {
			fmt.Fprintf(&want, "valid %d\n", utf8.RuneCount(b))
			valid++
		} else {
			want.WriteString("invalid none\n")
			invalid++
		}
	}
	src.WriteString("    return 0i32\n}\n")
	if valid < 50 || invalid < 50 {
		t.Fatalf("%d valid and %d invalid strings; want at least 50 of each", valid, invalid)
	}
	files := []*source.File{{Name: "utf8.kern", Bytes: []byte(src.String())}}
	prog := resolve.Resolve(files)
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %s", ds[0].Message)
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if err := opt.Run(mod, opt.Options{}); err != nil {
		t.Fatalf("opt: %v", err)
	}
	var out, errb bytes.Buffer
	if status := interp.New(mod, "utf8.kern", &out, &errb).Run(); status != 0 {
		t.Fatalf("interpreter status %d: %s", status, errb.String())
	}
	got, exp := strings.Split(out.String(), "\n"), strings.Split(want.String(), "\n")
	if len(got) != len(exp) {
		t.Fatalf("%d output lines, want %d", len(got), len(exp))
	}
	for i := range exp {
		if got[i] != exp[i] {
			t.Errorf("string %d % x: kern %q, Go %q", i, cases[i], got[i], exp[i])
		}
	}
}
