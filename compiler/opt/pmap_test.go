package opt

import (
	"math/rand"
	"testing"
)

// intHash spreads ints; fewHash folds them onto four hashes, so that most
// keys collide all the way down (specs/prove-state §3).
type intHash struct{}

func (intHash) hash(k int) uint64 { return mix(uint64(k)) } //nolint:gosec // test keys

type fewHash struct{}

func (fewHash) hash(k int) uint64 { return uint64(k % 4) } //nolint:gosec // as above

// checkSame compares a map with a Go map: size, every key, no extra key.
func checkSame[H hasher[int]](t *testing.T, m pmap[int, int, H], want map[int]int, what string) {
	t.Helper()
	if m.size() != len(want) {
		t.Fatalf("%s: size %d, want %d", what, m.size(), len(want))
	}
	for k, v := range want {
		if got, ok := m.get(k); !ok || got != v {
			t.Fatalf("%s: get(%d) = %d, %v; want %d", what, k, got, ok, v)
		}
	}
	n := 0
	m.each(func(k, v int) {
		n++
		if want[k] != v {
			t.Fatalf("%s: each yields %d -> %d, want %d", what, k, v, want[k])
		}
	})
	if n != len(want) {
		t.Fatalf("%s: each yields %d entries, want %d", what, n, len(want))
	}
}

func clone(m map[int]int) map[int]int {
	out := make(map[int]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Random inserts and deletes agree with a Go map at every step, and every
// earlier version stays as it was.
func testPmapOps[H hasher[int]](t *testing.T, seed int64) {
	r := rand.New(rand.NewSource(seed))
	var m pmap[int, int, H]
	want := map[int]int{}
	type version struct {
		m    pmap[int, int, H]
		want map[int]int
	}
	var versions []version
	for step := 0; step < 3000; step++ {
		k := r.Intn(300)
		if r.Intn(3) == 0 {
			m = m.del(k)
			delete(want, k)
		} else {
			v := r.Intn(5)
			m = m.set(k, v)
			want[k] = v
		}
		if step%250 == 0 {
			versions = append(versions, version{m, clone(want)})
		}
	}
	checkSame(t, m, want, "final")
	for i, v := range versions {
		checkSame(t, v.m, v.want, "an earlier version")
		_ = i
	}
	// a set of an unchanged value and a delete of an absent key return
	// the map itself
	for k, v := range want {
		if m.set(k, v).root != m.root {
			t.Fatalf("set of an unchanged value made a new map")
		}
		break
	}
	if m.del(1000).root != m.root {
		t.Fatalf("delete of an absent key made a new map")
	}
}

func TestPmapOps(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		testPmapOps[intHash](t, seed)
		testPmapOps[fewHash](t, seed)
	}
}

// intersect agrees with a reference over Go maps; a shared map is
// returned as it is.
func testPmapIntersect[H hasher[int]](t *testing.T, seed int64) {
	r := rand.New(rand.NewSource(seed))
	var base pmap[int, int, H]
	ref := map[int]int{}
	for i := 0; i < 200; i++ {
		k, v := r.Intn(400), r.Intn(10)
		base = base.set(k, v)
		ref[k] = v
	}
	// two maps diverging from base
	a, b := base, base
	ra, rb := clone(ref), clone(ref)
	for i := 0; i < 40; i++ {
		k, v := r.Intn(400), r.Intn(10)
		switch r.Intn(4) {
		case 0:
			a = a.del(k)
			delete(ra, k)
		case 1:
			b = b.del(k)
			delete(rb, k)
		case 2:
			a = a.set(k, v)
			ra[k] = v
		default:
			b = b.set(k, v)
			rb[k] = v
		}
	}
	minOf := func(_ int, x, y int) (int, bool) { return min(x, y), true }
	sameOnly := func(_ int, x, y int) (int, bool) { return x, x == y }
	for name, c := range map[string]func(int, int, int) (int, bool){"min": minOf, "same": sameOnly} {
		got := intersect(a, b, c)
		want := map[int]int{}
		for k, x := range ra {
			if y, ok := rb[k]; ok {
				if v, keep := c(k, x, y); keep {
					want[k] = v
				}
			}
		}
		checkSame(t, got, want, "intersect "+name)
		checkSame(t, a, ra, "a after intersect")
		checkSame(t, b, rb, "b after intersect")
		if self := intersect(a, a, c); self.root != a.root {
			t.Fatalf("intersect %s of a map with itself made a new map", name)
		}
	}
	if !equal(a, a, func(x, y int) bool { return x == y }) {
		t.Fatalf("a map is not equal to itself")
	}
	if equal(a, b, func(x, y int) bool { return x == y }) != mapsEqual(ra, rb) {
		t.Fatalf("equal disagrees with the reference")
	}
	rebuilt := pmap[int, int, H]{}
	for k, v := range ra {
		rebuilt = rebuilt.set(k, v)
	}
	if !equal(a, rebuilt, func(x, y int) bool { return x == y }) {
		t.Fatalf("a map rebuilt in another order is not equal")
	}
}

func mapsEqual(x, y map[int]int) bool {
	if len(x) != len(y) {
		return false
	}
	for k, v := range x {
		if w, ok := y[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestPmapIntersect(t *testing.T) {
	for seed := int64(1); seed <= 10; seed++ {
		testPmapIntersect[intHash](t, seed)
		testPmapIntersect[fewHash](t, seed)
	}
}

// A subtree two maps share is the intersection's, pointer for pointer.
func TestPmapIntersectShares(t *testing.T) {
	var base pmap[int, int, intHash]
	for i := 0; i < 1000; i++ {
		base = base.set(i, i)
	}
	a, b := base.set(5000, 1), base.set(6000, 2)
	got := intersect(a, b, func(_ int, x, y int) (int, bool) { return x, x == y })
	if got.size() != 1000 {
		t.Fatalf("size %d, want 1000", got.size())
	}
	shared := 0
	for i, s := range got.root.slots {
		if s.sub != nil && s.sub == base.root.slots[i].sub {
			shared++
		}
	}
	if shared < len(base.root.slots)-2 {
		t.Fatalf("only %d of %d top-level subtrees are shared with the base", shared, len(base.root.slots))
	}
}
