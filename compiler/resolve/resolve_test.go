package resolve

import (
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/source"
)

func resolveSrc(t *testing.T, src string) *Program {
	t.Helper()
	return Resolve([]*source.File{{Name: "t.kern", Bytes: []byte(src)}})
}

func codes(p *Program) string {
	var out []string
	for _, d := range p.Diags {
		out = append(out, string(d.Code)+"@"+d.Message)
	}
	return strings.Join(out, "\n")
}

func TestResolveBasics(t *testing.T) {
	p := resolveSrc(t, `module m
struct Page { id: u64; data: [8]u8 }
struct Node { keys: [4]u64; count: u32 }

func checksum(data: view[]u8) -> u64 {
    total := 0u64
    for b in data { total = total + u64(b) }
    return total
}

func sum_page(p: view[Page]) -> u64 {
    return checksum(p.data) + p.id
}

func make(id: u64) -> Page {
    return Page { id: id, data: buf() }
}

func buf() -> [8]u8 { var b: [8]u8; return b }

func split(pool: edit[Pool[Node]], a: PoolId[Node], b: PoolId[Node]) -> Result[u32, AllocErr] {
    with l, r := pool.get2(a, b) {
        r.count = l.count
        l.count = 0u32
    }
    n := Node { keys: pool.get(a).keys, count: 1u32 }
    id := pool.alloc(n)?
    match pool.alloc(Node { keys: pool.get(id).keys, count: 2u32 }) {
        Ok(x) => return Ok(x.index)
        Err(e) => return Err(e)
    }
}

func go_thread(v: own Vec[u64]) -> usize { return v.len() }

func spawn_it(v: own Vec[u64]) -> Result[usize, SpawnErr] {
    t := thread.spawn(go_thread, v)?
    return Ok(t.join())
}
`)
	if len(p.Diags) != 0 {
		t.Fatalf("unexpected diagnostics:\n%s", codes(p))
	}
}

func TestResolveErrors(t *testing.T) {
	cases := map[string]string{
		"TYPE_MISMATCH":            "module m\nfunc f() -> u64 { return true }\n",
		"NAME_UNRESOLVED":          "module m\nfunc f() -> u64 { return zz }\n",
		"PARAM_OWN_REQUIRED":       "module m\nfunc f(v: Vec[u64]) { }\n",
		"ACC_UNBOUND_PROJECTION":   "module m\nfunc f(v: edit[Vec[Box[u64]]]) { x := v.at(0usize) }\n",
		"MATCH_NOT_EXHAUSTIVE":     "module m\nfunc f(r: view[Result[u64, u8]]) { match r { Ok(v) => {} } }\n",
		"CONSTRAINT_UNSATISFIED":   "module m\nfunc g[T: Copy](x: T) { }\nfunc f(v: own Vec[u64]) { g[Vec[u64]](v) }\n",
		"FFI_UNSUPPORTED_TYPE":     "module m\nextern \"C\" func p(v: view[u8]) -> i32\n",
		"UNSAFE_OP_OUTSIDE_UNSAFE": "module m\nextern \"C\" func p(v: i32) -> i32\nfunc f() -> i32 { return p(1i32) }\n",
		"SEND_NOT_SEND":            "module m\nfunc w(p: own RawPtr[u8]) -> u64 { return 0u64 }\nfunc f(p: RawPtr[u8]) { r := thread.spawn(w, p) }\n",
		"TYPE_QUESTION_MISMATCH":   "module m\nfunc g() -> Result[u64, u8] { return Ok(1u64) }\nfunc f() -> u64 { return g()? }\n",
	}
	for want, src := range cases {
		p := resolveSrc(t, src)
		found := false
		for _, d := range p.Diags {
			if string(d.Code) == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected %s, got:\n%s", want, want, codes(p))
		}
	}
}
