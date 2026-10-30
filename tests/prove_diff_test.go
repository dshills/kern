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

// TestProvenChecksDifferential is the soundness test of the proof pass and
// the inliner (spec §3.3: a proof the compiler establishes is never a
// heuristic). It generates random programs that index a vector under the
// guards the proof pass reasons about (length tests on both edges, loop
// bounds, dominating checks, constant lengths, sub-slices, helpers the
// release build inlines, and pushes and writes between them that must
// forget facts), runs each in the interpreter, which executes every check,
// and then natively in both optimisation levels, where a proven check is
// skipped: output, exit status and the trap record must agree. A proof
// that was wrong shows as a native run that reads past the end where the
// interpreter trapped, or traps at a different site.
//
// KERN_DIFF_PROGRAMS sets the number of programs (default 40) and
// KERN_DIFF_SEED the seed of the first one; a failure prints the program.
func TestProvenChecksDifferential(t *testing.T) {
	if !native.Available() {
		t.Skip("clang not available")
	}
	n := 40
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
			src := genProgram(rand.New(rand.NewSource(s)))
			if os.Getenv("KERN_DIFF_DUMP") != "" {
				t.Log(src)
			}
			f := &source.File{Name: "diff.kern", Bytes: []byte(src)}
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
			it := interp.New(mod, "diff.kern", &out, &errb)
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

// genProgram writes one random program. The vector holds n values; every
// index is a literal or a derived value near n, so about half the reads
// are in bounds and the rest trap somewhere the proof pass must not have
// removed the check.
func genProgram(r *rand.Rand) string {
	n := r.Intn(7) // 0..6 elements; the empty vector traps on its first read
	var b strings.Builder
	b.WriteString(`module d
func rd(p: view[]u64, at: usize) -> u64 { return p[at] }
func pair(p: view[]u64, at: usize) -> u64 { return wrapping_add(p[at], p[at + 1usize]) }
func wr(p: edit[]u64, at: usize, v: u64) { p[at] = v }
func sum_to(p: view[]u64, n: usize) -> u64 {
    s := 0u64
    i := 0usize
    while i < n {
        s = wrapping_add(s, p[i])
        i = i + 1usize
    }
    return s
}
func head(p: view[]u64) -> u64 {
    if len(p) < 3usize { return 0u64 }
    return wrapping_add(p[2usize], rd(p, 1usize))
}
func tail(p: view[]u64) -> u64 {
    if len(p) >= 2usize { return p[len(p) - 2usize] }
    return 7u64
}
func cached(p: view[]u64) -> u64 {
    m := len(p)
    s := 0u64
    i := 0usize
    while i < m {
        s = wrapping_add(s, p[i])
        i = i + 1usize
    }
    return s
}
func window(p: view[]u64) -> u64 {
    s := 0u64
    i := 0usize
    while i + 2usize <= len(p) {
        s = wrapping_add(s, wrapping_add(p[i], p[i + 1usize]))
        i = i + 2usize
    }
    return s
}
func window_past(p: view[]u64) -> u64 {
    s := 0u64
    i := 0usize
    while i + 2usize <= len(p) {
        s = wrapping_add(s, p[i + 2usize])
        i = i + 2usize
    }
    return s
}
func eq4(p: view[]u64) -> u64 {
    if len(p) == 4usize { return p[3usize] }
    return p[4usize]
}
func both(p: view[]u64, q: view[]u64) -> u64 {
    s := 0u64
    i := 0usize
    while i < len(p) && i < len(q) {
        s = wrapping_add(s, wrapping_mul(p[i], q[i]))
        i = i + 1usize
    }
    return s
}
func lockstep(p: view[]u64, q: view[]u64) -> u64 {
    s := 0u64
    i := 0usize
    while i < len(p) {
        s = wrapping_add(s, wrapping_add(p[i], q[i]))
        i = i + 1usize
    }
    return s
}
func either(p: view[]u64, i: usize) -> u64 {
    if i >= len(p) || p[i] == 0u64 { return 1u64 }
    return p[i]
}
func prev(p: view[]u64, i: usize) -> u64 {
    if i < len(p) {
        if i >= 1usize { return p[i - 1usize] }
    }
    return 5u64
}
func stride(p: view[]u64, i: usize) -> u64 {
    if i * 2usize + 1usize < len(p) { return p[i * 2usize + 1usize] }
    return p[i * 2usize]
}
func put2(e: edit[]u64, i: usize, a: u64, b: u64) {
    if i + 1usize < len(e) {
        e[i] = a
        e[i + 1usize] = b
    } else {
        e[i] = a
    }
}
func after(e: edit[]u64, i: usize) -> u64 {
    if i < len(e) {
        wr(e, 0usize, 9u64)
        return e[i]
    }
    return e[i]
}
func mid(p: view[]u64, i: usize) -> u64 {
    if i + 2usize <= len(p) { return sum_to(p[i : i + 2usize], 2usize) }
    return sum_to(p[i : len(p)], 1usize)
}
func arr8(idx: usize) -> u64 {
    var a: [8]u64 = zero()
    a[2usize] = 4u64
    if idx < 8usize { return a[idx] }
    return a[idx - 8usize]
}
func marr(x: usize, y: u64) -> u64 {
    var a: [8]u64 = zero()
    a[3usize] = 7u64
    a[7usize] = 9u64
    s := wrapping_add(a[x & 7usize], a[(x & 3usize) + 4usize])
    j := x & 7usize
    s = wrapping_add(s, a[j])
    return wrapping_add(s, a[usize(y & 7u64)])
}
func mtrap(x: usize, which: usize) -> u64 {
    var a: [8]u64 = zero()
    a[1usize] = 2u64
    if which == 0usize { return a[x & 8usize] }
    if which == 1usize { return a[x & 15usize] }
    return a[(x & 7usize) + 1usize]
}
func mslice(p: view[]u64, x: usize) -> u64 {
    if len(p) >= 4usize { return p[x & 3usize] }
    return p[x & 7usize]
}
func main() -> i32 {
    v := Vec[u64].new()
    q := Vec[u64].new()
`)
	qn := r.Intn(7)
	for range qn {
		fmt.Fprintf(&b, "    q.push(%du64)\n", r.Intn(100))
	}
	for range n {
		fmt.Fprintf(&b, "    v.push(%du64)\n", r.Intn(100))
	}
	live := n // the length the generator believes, to aim indexes near it
	stmts := 2 + r.Intn(6)
	for k := 0; k < stmts; k++ {
		idx := func() int { return r.Intn(live + 2) } // 0..live+1: past the end about a third of the time
		switch r.Intn(29) {
		case 26:
			fmt.Fprintf(&b, "    print_u64(marr(%dusize, %du64))\n", r.Intn(100), r.Intn(100))
		case 27:
			// masks that reach the length must stay checked: they trap when x has the high bits
			fmt.Fprintf(&b, "    print_u64(mtrap(%dusize, %dusize))\n", r.Intn(32), r.Intn(3))
		case 28:
			fmt.Fprintf(&b, "    with d := v.as_view() { print_u64(mslice(d, %dusize)) }\n", r.Intn(16))
		case 15:
			b.WriteString("    with d := v.as_view() { print_u64(cached(d)) }\n")
		case 16:
			b.WriteString("    with d := v.as_view() { print_u64(window(d)) }\n")
		case 17:
			// p[i + 2] under `i + 2 <= len` must stay checked: it traps whenever the loop runs
			b.WriteString("    with d := v.as_view() { print_u64(window_past(d)) }\n")
		case 18:
			b.WriteString("    with d := v.as_view() { print_u64(eq4(d)) }\n")
		case 19:
			b.WriteString("    with d := v.as_view() { with e := q.as_view() { print_u64(both(d, e)) } }\n")
		case 20:
			// q[i] under p's guard alone must stay checked: it traps when q is shorter
			b.WriteString("    with d := v.as_view() { with e := q.as_view() { print_u64(lockstep(d, e)) } }\n")
		case 21:
			fmt.Fprintf(&b, "    with d := v.as_view() { print_u64(wrapping_add(either(d, %dusize), prev(d, %dusize))) }\n", idx(), idx())
		case 22:
			fmt.Fprintf(&b, "    with d := v.as_view() { print_u64(stride(d, %dusize)) }\n", r.Intn(4))
		case 23:
			fmt.Fprintf(&b, "    with e := v.as_edit() { put2(e, %dusize, %du64, %du64) }\n", idx(), r.Intn(100), r.Intn(100))
		case 24:
			fmt.Fprintf(&b, "    with e := v.as_edit() { print_u64(after(e, %dusize)) }\n", idx())
		case 25:
			fmt.Fprintf(&b, "    with d := v.as_view() { print_u64(wrapping_add(mid(d, %dusize), arr8(%dusize))) }\n", idx(), r.Intn(10))
		case 0:
			fmt.Fprintf(&b, "    with d := v.as_view() { print_u64(rd(d, %dusize)) }\n", idx())
		case 1:
			fmt.Fprintf(&b, "    with d := v.as_view() { print_u64(pair(d, %dusize)) }\n", idx())
		case 2:
			g := r.Intn(live + 2)
			j := r.Intn(g + 2)
			fmt.Fprintf(&b, "    with d := v.as_view() { if len(d) >= %dusize { print_u64(d[%dusize]) } else { print_u64(1u64) } }\n", g, j)
		case 3:
			g := r.Intn(live + 2)
			j := r.Intn(g + 2)
			fmt.Fprintf(&b, "    with d := v.as_view() { if len(d) < %dusize { print_u64(2u64) } else { print_u64(rd(d, %dusize)) } }\n", g, j)
		case 4:
			fmt.Fprintf(&b, "    with d := v.as_view() { print_u64(sum_to(d, %dusize)) }\n", idx())
		case 5:
			b.WriteString("    with d := v.as_view() { print_u64(head(d)) }\n")
		case 6:
			b.WriteString("    with d := v.as_view() { print_u64(tail(d)) }\n")
		case 7:
			lo := r.Intn(live + 2)
			hi := lo + r.Intn(live+2-lo)
			c := r.Intn(hi - lo + 2)
			fmt.Fprintf(&b, "    with d := v.as_view() { with s := d[%dusize : %dusize] { print_u64(rd(s, %dusize)) } }\n", lo, hi, c)
		case 8:
			b.WriteString("    with d := v.as_view() { for i in 0usize..len(d) { print_u64(d[i]) } }\n")
		case 9:
			fmt.Fprintf(&b, "    with d := v.as_view() { i := %dusize\n        if i < len(d) { print_u64(d[i]) } else { print_u64(3u64) } }\n", idx())
		case 10:
			fmt.Fprintf(&b, "    v.push(%du64)\n", r.Intn(100))
			live++
		case 11:
			fmt.Fprintf(&b, "    with e := v.as_edit() { wr(e, %dusize, %du64) }\n", idx(), r.Intn(100))
		case 12:
			// a fixed array: reads at literal indexes are proven by the
			// constant length, or trap past it
			fmt.Fprintf(&b, "    {\n        var a: [4]u64 = zero()\n        a[%dusize] = %du64\n        print_u64(wrapping_add(a[%dusize], rd(a, %dusize)))\n    }\n", r.Intn(4), r.Intn(100), r.Intn(6), r.Intn(6))
		case 13:
			// byte-order reads of two, four or eight bytes at an offset of a
			// byte array: one check on the last byte, past the end about half
			// the time
			w := []string{"u16", "u32", "u64"}[r.Intn(3)]
			order := []string{"load_be", "load_le"}[r.Intn(2)]
			fmt.Fprintf(&b, "    {\n        var y: [8]u8 = zero()\n        y[%dusize] = %du8\n        y[7usize] = %du8\n        print_u64(u64(%s[%s](y, %dusize)))\n    }\n", r.Intn(8), r.Intn(256), r.Intn(256), order, w, r.Intn(9))
		case 14:
			// a byte-order write followed by a read of the whole array as one
			// word: the write's check on its last byte, past the end about
			// half the time
			w := []string{"u16", "u32", "u64"}[r.Intn(3)]
			order := []string{"be", "le"}[r.Intn(2)]
			fmt.Fprintf(&b, "    {\n        var z: [8]u8 = zero()\n        store_%s[%s](z, %dusize, %d%s)\n        print_u64(load_le[u64](z, 0usize))\n    }\n", order, w, r.Intn(9), r.Intn(60000), w)
		}
		b.WriteString("    println()\n")
	}
	b.WriteString("    return 0i32\n}\n")
	return b.String()
}
