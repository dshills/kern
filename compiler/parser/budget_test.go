package parser

import (
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/source"
)

// budgetOf parses src and returns the budget of the function named name.
func budgetOf(t *testing.T, src, name string) *ast.FuncDecl {
	t.Helper()
	f, ds := Parse(&source.File{Name: "b.kern", Bytes: []byte(src)})
	if len(ds) != 0 {
		t.Fatalf("%q: %s", src, ds[0].Message)
	}
	var found *ast.FuncDecl
	ast.Walk(f, func(n any) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Name == name {
			found = fd
			return true
		}
		return false
	})
	if found == nil {
		t.Fatalf("%q: no function %s", src, name)
	}
	return found
}

// Budget blocks parse in every accepted form (spec §3, R1): both
// separators, a trailing separator, an empty block, after a contract and
// inside impl, with each item's name, value and spans recorded.
func TestBudgetForms(t *testing.T) {
	cases := []struct {
		name, src string
		items     []string // name=value
		contract  bool
	}{
		{"semicolons", "module m\nbudget { bounds_checks 0; copies 1; allocations 2 }\nfunc f() { }\n", []string{"bounds_checks=0", "copies=1", "allocations=2"}, false},
		{"newlines", "module m\nbudget {\n    bounds_checks 0\n    copies 1\n}\nfunc f() { }\n", []string{"bounds_checks=0", "copies=1"}, false},
		{"trailing semicolon", "module m\nbudget { copies 3; }\nfunc f() { }\n", []string{"copies=3"}, false},
		{"trailing newline mixed", "module m\nbudget {\n    copies 3;\n    allocations 1_000\n\n}\nfunc f() { }\n", []string{"copies=3", "allocations=1_000"}, false},
		{"empty", "module m\nbudget { }\nfunc f() { }\n", nil, false},
		{"empty no space", "module m\nbudget {}\nfunc f() { }\n", nil, false},
		{"same line", "module m\nbudget { copies 0 } func f() { }\n", []string{"copies=0"}, false},
		{"suffixed value kept", "module m\nbudget { copies 0u64 }\nfunc f() { }\n", []string{"copies=0u64"}, false},
		{"unknown name kept", "module m\nbudget { loops 1; copies 1; copies 2 }\nfunc f() { }\n", []string{"loops=1", "copies=1", "copies=2"}, false},
		{"after contract", "module m\ncontract { requires \"x\" }\nbudget { copies 0 }\nfunc f() { }\n", []string{"copies=0"}, true},
		{"in impl", "module m\nstruct S { a: u64 }\nimpl S {\n    budget { bounds_checks 0 }\n    func f(view self) { }\n}\n", []string{"bounds_checks=0"}, false},
		{"in impl after contract", "module m\nstruct S { a: u64 }\nimpl S {\n    contract { requires \"x\" }\n    budget { bounds_checks 0 }\n    func f(view self) { }\n}\n", []string{"bounds_checks=0"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fd := budgetOf(t, c.src, "f")
			if fd.Budget == nil {
				t.Fatalf("no budget")
			}
			if (fd.Contract != nil) != c.contract {
				t.Fatalf("contract: %v", fd.Contract)
			}
			var got []string
			for _, it := range fd.Budget.Items {
				got = append(got, it.Name+"="+it.Value)
				if c.src[it.NamePos.Start:it.NamePos.End] != it.Name {
					t.Errorf("name span %v covers %q", it.NamePos, c.src[it.NamePos.Start:it.NamePos.End])
				}
				if !strings.HasSuffix(c.src[it.Start:it.End], it.Value) || it.Start != it.NamePos.Start {
					t.Errorf("item span %v covers %q", it.Pos, c.src[it.Start:it.End])
				}
			}
			if strings.Join(got, ",") != strings.Join(c.items, ",") {
				t.Fatalf("items %v, want %v", got, c.items)
			}
			if b := c.src[fd.Budget.Start:fd.Budget.End]; !strings.HasPrefix(b, "budget") || !strings.HasSuffix(b, "}") {
				t.Fatalf("budget span covers %q", b)
			}
			want := fd.Budget.Start
			if fd.Contract != nil {
				want = fd.Contract.Start
			}
			if fd.Start != want {
				t.Fatalf("declaration starts at %d, want %d", fd.Start, want)
			}
		})
	}
}

// budget stays an ordinary identifier everywhere but a declaration start
// followed by `{` (spec §3).
func TestBudgetIdentifier(t *testing.T) {
	src := `module m
struct S { budget: u64; copies: u64 }
func budget(budget: u64) -> u64 {
    copies := budget
    allocations := S { budget: copies, copies: 1 }
    budget = allocations.budget
    return budget
}
func g() -> u64 { return budget(1) }
`
	fd := budgetOf(t, src, "budget")
	if fd.Budget != nil || len(fd.Params) != 1 || fd.Params[0].Name != "budget" {
		t.Fatalf("budget function parsed as %+v", fd)
	}
	if g := budgetOf(t, src, "g"); g.Budget != nil {
		t.Fatalf("g has a budget")
	}
}

// What the grammar rejects is a SYNTAX error (R2): a budget block before
// anything but func, an item that is not an identifier followed by an
// integer literal, and an unclosed block.
func TestBudgetSyntaxErrors(t *testing.T) {
	cases := map[string]string{
		"before struct":       "module m\nbudget { copies 0 }\nstruct S { a: u64 }\n",
		"before union":        "module m\nbudget { copies 0 }\nunion U { A }\n",
		"before interface":    "module m\nbudget { copies 0 }\ninterface I { func f(view self) }\n",
		"before const":        "module m\nbudget { copies 0 }\nconst K: u64 = 1\n",
		"before thread_local": "module m\nbudget { copies 0 }\nthread_local G: u64 = 1\n",
		"before contract":     "module m\nbudget { copies 0 }\ncontract { requires \"x\" }\nfunc f() { }\n",
		"before extern":       "module m\nbudget { copies 0 }\nextern \"C\" func puts(s: RawPtr[u8]) -> i32\n",
		"before impl":         "module m\nbudget { copies 0 }\nimpl S { }\n",
		"at end of file":      "module m\nbudget { copies 0 }\n",
		"before destructor":   "module m\nstruct S { a: u64 }\nimpl S {\n    budget { copies 0 }\n    destructor(edit self) { }\n}\n",
		"on interface method": "module m\ninterface I {\n    budget { copies 0 }\n    func f(view self)\n}\n",
		"twice":               "module m\nbudget { copies 0 }\nbudget { allocations 0 }\nfunc f() { }\n",
		"name value":          "module m\nbudget { bounds_checks n }\nfunc f() { }\n",
		"negative":            "module m\nbudget { bounds_checks -1 }\nfunc f() { }\n",
		"float":               "module m\nbudget { bounds_checks 1.5 }\nfunc f() { }\n",
		"missing value":       "module m\nbudget { bounds_checks }\nfunc f() { }\n",
		"two on a line":       "module m\nbudget { copies 0 allocations 0 }\nfunc f() { }\n",
		"comma":               "module m\nbudget { copies 0, allocations 0 }\nfunc f() { }\n",
		"missing brace":       "module m\nbudget { copies 0\nfunc f() { }\n",
		"brace on next line":  "module m\nbudget\n{ copies 0 }\nfunc f() { }\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, ds := Parse(&source.File{Name: "b.kern", Bytes: []byte(src)})
			if len(ds) != 1 || ds[0].Code != "SYNTAX" {
				t.Fatalf("expected one SYNTAX diagnostic, got %v", ds)
			}
		})
	}
	_, ds := Parse(&source.File{Name: "b.kern", Bytes: []byte("module m\nbudget { copies 0 }\nstruct S { a: u64 }\n")})
	if len(ds) == 1 && !strings.Contains(ds[0].Message, "a budget block must precede a func declaration") {
		t.Fatalf("message %q", ds[0].Message)
	}
}
