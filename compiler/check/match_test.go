package check

import (
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

func checkSrc(t *testing.T, src string) []diag.Diagnostic {
	t.Helper()
	prog := resolve.Resolve([]*source.File{{Name: "t.kern", Bytes: []byte(src)}})
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s: %s", prog.Diags[0].Code, prog.Diags[0].Message)
	}
	return Check(prog)
}

// A conflict with a freeze that exists only because an arm wrote its Copy
// binding says where that write is (specs/match-bindings M7).
func TestMatchWrittenBindingConflict(t *testing.T) {
	label := func(name string) string {
		return "the arm writes " + name + " here, which makes " + name + " an edit of the matched place"
	}
	cases := []struct {
		name, src string
		related   string // the text the related span covers; "" for none
		label     string
	}{
		{"one write", `module m
func f(o: edit[Option[u64]]) {
    match o {
        Some(n) => {
            n = n + 1u64
            o = None
        }
        None => { }
    }
}
`, "n", label("n")},
		{"several edit uses of one binding", `module m
func add(v: edit[u64], k: u64) -> u64 { v = v + k; return v }
func apply(f: closure()) { f() }
func f(o: edit[Option[u64]]) {
    match o {
        Some(n) => {
            print_u64(add(n, 1u64))
            apply(|| { n = 2u64 })
            n = 3u64
            o = None
        }
        None => { }
    }
}
`, "n", label("n")},
		{"two written bindings", `module m
union U { Two(a: u64, b: u64); Empty }
func f(u: edit[U]) {
    match u {
        Two(a, b) => {
            b = 1u64
            a = 2u64
            u = U.Empty
        }
        Empty => { }
    }
}
`, "b", label("b")},
		{"through a nested match", `module m
func f(o: edit[Option[Option[u64]]]) {
    match o {
        Some(inner) => {
            match inner {
                Some(x) => { x = 1u64 }
                None => { }
            }
            o = None
        }
        None => { }
    }
}
`, "x", label("inner")},
		{"a binding that is not Copy", `module m
import "std/str"
union S { Full(s: str.String, n: u64); Empty }
func f(u: edit[S]) {
    match u {
        Full(s, n) => {
            n = 1u64
            u = S.Empty
        }
        Empty => { }
    }
}
`, "", ""},
	}
	for _, k := range cases {
		ds := checkSrc(t, k.src)
		if len(ds) != 1 || ds[0].Code != "ACC_CONFLICT" {
			t.Errorf("%s: diagnostics %+v, want one ACC_CONFLICT", k.name, ds)
			continue
		}
		rel := ds[0].Related
		if k.related == "" {
			if len(rel) != 0 {
				t.Errorf("%s: related %+v, want none", k.name, rel)
			}
			continue
		}
		// the first edit use in the source: the first occurrence of the
		// name as an edit, which in these programs is its first occurrence
		// inside the arm's body
		body := strings.Index(k.src, "=> {")
		want := body + strings.Index(k.src[body:], " "+k.related+" ") + 1
		if k.name == "several edit uses of one binding" {
			want = strings.Index(k.src, "add(n, 1u64)") + len("add(")
		}
		if k.name == "through a nested match" {
			want = strings.Index(k.src, "x = 1u64")
		}
		if len(rel) != 1 || rel[0].Label != k.label || rel[0].Start != want || rel[0].End != want+len(k.related) {
			t.Errorf("%s: related %+v, want %q at %d", k.name, rel, k.label, want)
		}
	}
}

// An arm that holds no access binding leaves the subject free (M6); an arm
// that holds one does not.
func TestMatchFreezesArmByArm(t *testing.T) {
	free := `module m
import "std/str"
union S { Full(s: str.String); Empty }
func f(u: edit[S], o: edit[Option[u64]]) {
    match u {
        Full(s) => { s.push(65u8) }
        Empty => { u = S.Full(str.String.new()) }
    }
    match o {
        Some(n) => { n = n + 1u64 }
        None => { o = Some(0u64) }
    }
    match o {
        Some(n) => { o = Some(n + 1u64) }
        None => { }
    }
}
`
	if ds := checkSrc(t, free); len(ds) != 0 {
		t.Errorf("arms without an access binding: %+v", ds)
	}
	frozen := `module m
import "std/str"
union S { Full(s: str.String); Empty }
func f(u: edit[S]) {
    match u {
        Full(s) => { u = S.Empty }
        Empty => { }
    }
}
`
	if ds := checkSrc(t, frozen); len(ds) != 1 || ds[0].Code != "ACC_CONFLICT" {
		t.Errorf("an arm with an access binding: %+v", ds)
	}
}
