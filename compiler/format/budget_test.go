package format

import (
	"fmt"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

func dumpOf(t *testing.T, name string, src []byte) string {
	t.Helper()
	f, ds := parser.Parse(&source.File{Name: name, Bytes: src})
	if len(ds) != 0 {
		t.Fatalf("parse %s: %s\n%s", name, ds[0].Message, src)
	}
	return ast.Dump(f)
}

// Budget blocks round-trip through the formatter (spec R1): each item name,
// both separators, with and without a trailing separator, with and without
// a contract, at top level and in impl, plus an empty block. The dump of
// the formatted file equals the dump of the input, the output holds the
// canonical one-line block, and formatting is idempotent.
func TestBudgetRoundTrip(t *testing.T) {
	names := []string{"bounds_checks", "copies", "allocations"}
	type form struct {
		label, block, canon string
	}
	var forms []form
	for i, n := range names {
		other := names[(i+1)%len(names)]
		for _, sep := range []string{";", "\n"} {
			for _, trailing := range []bool{false, true} {
				body := fmt.Sprintf("%s %d%s%s 7", n, i, sep, other)
				if trailing {
					body += sep
				}
				block := "budget { " + body + " }"
				if sep == "\n" {
					block = "budget {\n" + body + "\n}"
				}
				forms = append(forms, form{
					label: fmt.Sprintf("%s/%q/trailing=%v", n, sep, trailing),
					block: block,
					canon: fmt.Sprintf("budget { %s %d; %s 7 }", n, i, other),
				})
			}
		}
	}
	forms = append(forms, form{"empty", "budget { }", "budget { }"}, form{"empty-tight", "budget {}", "budget { }"}, form{"empty-lines", "budget {\n\n}", "budget { }"})
	for _, fm := range forms {
		for _, contract := range []bool{false, true} {
			for _, inImpl := range []bool{false, true} {
				fm, contract, inImpl := fm, contract, inImpl
				t.Run(fmt.Sprintf("%s/contract=%v/impl=%v", fm.label, contract, inImpl), func(t *testing.T) {
					head := fm.block + "\n"
					if contract {
						head = "contract { requires \"x\" }\n" + head
					}
					var src, want string
					if inImpl {
						src = "module m\nstruct S { a: u64 }\nimpl S {\n" + head + "func f(view self) -> u64 { return self.a }\n}\n"
						canon := "    " + fm.canon + "\n"
						if contract {
							canon = "    contract { requires \"x\" }\n" + canon
						}
						want = "module m\nstruct S { a: u64 }\nimpl S {\n" + canon + "    func f(view self) -> u64 { return self.a }\n}\n"
					} else {
						src = "module m\n" + head + "func f() -> u64 { return 0u64 }\n"
						canon := fm.canon + "\n"
						if contract {
							canon = "contract { requires \"x\" }\n" + canon
						}
						want = "module m\n" + canon + "func f() -> u64 { return 0u64 }\n"
					}
					out, ds := Format(&source.File{Name: "b.kern", Bytes: []byte(src)})
					if ds != nil {
						t.Fatalf("diagnostics: %s", ds[0].Message)
					}
					if string(out) != want {
						t.Fatalf("formatted:\n%s\nwant:\n%s", out, want)
					}
					if a, b := dumpOf(t, "in", []byte(src)), dumpOf(t, "out", out); a != b {
						t.Fatalf("dump changed:\n%s\n%s", a, b)
					}
					again, _ := Format(&source.File{Name: "b.kern", Bytes: out})
					if string(again) != string(out) {
						t.Fatalf("not idempotent:\n%s", again)
					}
					checkInvariants(t, out)
				})
			}
		}
	}
}

// Comments in and around a budget block are kept, and formatting them is
// idempotent: a block containing a comment is laid out one item per line,
// and a same-line comment attaches to the first item on its source line, as
// in any body.
func TestBudgetComments(t *testing.T) {
	src := "module m\n// about f\nbudget { // why\n    // the loop is proven\n    bounds_checks 0; copies 1 // two\n} // after\nfunc f() { }\n"
	out, ds := Format(&source.File{Name: "b.kern", Bytes: []byte(src)})
	if ds != nil {
		t.Fatalf("diagnostics: %s", ds[0].Message)
	}
	if got, want := commentTexts(out), commentTexts([]byte(src)); !equal(got, want) {
		t.Fatalf("comments %v, want %v\n%s", got, want, out)
	}
	want := "module m\n// about f\nbudget {  // why\n    // the loop is proven\n    bounds_checks 0  // two\n    copies 1\n}  // after\nfunc f() { }\n"
	if string(out) != want {
		t.Fatalf("formatted:\n%s\nwant:\n%s", out, want)
	}
	again, _ := Format(&source.File{Name: "b.kern", Bytes: out})
	if string(again) != string(out) {
		t.Fatalf("not idempotent:\n%s\nthen\n%s", out, again)
	}
	if a, b := dumpOf(t, "in", []byte(src)), dumpOf(t, "out", out); a != b {
		t.Fatalf("dump changed")
	}
}
