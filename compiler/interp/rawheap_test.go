package interp

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

var noPos ast.Pos

// Every size up to smallMax has a class that holds it, less than a quarter
// larger past the multiples of 16, and classSize is classOf's inverse.
func TestRawHeapClasses(t *testing.T) {
	last := -1
	for n := uint64(1); n <= smallMax; n++ {
		idx, size := classOf(n)
		if idx < last || idx > last+1 || idx >= numClasses {
			t.Fatalf("size %d: class %d after %d", n, idx, last)
		}
		last = idx
		if size < n || classSize(idx) != size || size%minBlock != 0 {
			t.Fatalf("size %d: class %d of %d bytes (classSize %d)", n, idx, size, classSize(idx))
		}
		if n > 128 && (size-n)*4 >= size {
			t.Fatalf("size %d: class of %d bytes wastes a quarter", n, size)
		}
	}
	if last != numClasses-1 {
		t.Fatalf("%d classes, want %d", last+1, numClasses)
	}
}

// What free, realloc and is reject, and what an access to a freed block
// reads while the block is quarantined.
func TestRawHeapDetections(t *testing.T) {
	h := newRawHeap()
	a := h.alloc(64, 8)
	b := h.alloc(64, 8)
	for i, x := range h.bytes(a, 64) {
		if x != 0 {
			t.Fatalf("byte %d of a new block is %d", i, x)
		}
	}
	copy(h.bytes(a, 64), "the dead block's own bytes")
	if h.is(a, 32, 8) || h.is(a, 64, 16) || h.is(a+8, 64, 8) || !h.is(a, 64, 8) {
		t.Fatal("is: only the block's start with its size and alignment is live")
	}
	if h.free(a + 8) {
		t.Fatal("free of an address inside a block")
	}
	if _, live := h.realloc(a+8, 64, 128, 8); live {
		t.Fatal("realloc of an address inside a block")
	}
	if !h.free(a) || h.free(a) || h.is(a, 64, 8) {
		t.Fatal("free succeeds once, and the block is then not live")
	}
	if _, live := h.realloc(a, 64, 128, 8); live {
		t.Fatal("realloc of a freed block")
	}
	if got := string(h.bytes(a, 26)); got != "the dead block's own bytes" {
		t.Fatalf("a quarantined block reads %q", got)
	}
	// its address is not given out while it is quarantined
	for i := 0; i < 1000; i++ {
		if c := h.alloc(64, 8); c == a || c == b || c == 0 {
			t.Fatalf("allocation %d is at %#x", i, c)
		}
	}
	// realloc moves, keeps the prefix and frees the old block
	n, live := h.realloc(b, 64, 4096, 8)
	if !live || n == 0 || n == b || h.is(b, 64, 8) || !h.is(n, 4096, 8) {
		t.Fatalf("realloc: %#x -> %#x", b, n)
	}
	// nothing maps address 0, the addresses below the heap or those past it
	if h.bytes(0, 1) != nil || h.bytes(heapBase-1, 1) != nil || h.bytes(heapBase+64<<20, 1) != nil {
		t.Fatal("an unmapped address is addressable")
	}
	// a range must end in the chunk it starts in
	if h.bytes(heapBase+chunkSize-4, 8) != nil || h.bytes(heapBase+chunkSize-8, 8) == nil {
		t.Fatal("a range past its chunk's end")
	}
	if h.alloc(8, 3) != 0 || h.alloc(8, 0) != 0 || h.alloc(8, chunkSize*2) != 0 || h.alloc(maxAllocSize+1, 8) != 0 {
		t.Fatal("a request that cannot be honoured must fail")
	}
}

// A freed small block keeps its bytes and its address until quarantineBytes
// have been freed after it; then it is given out again, cleared.
func TestRawHeapQuarantine(t *testing.T) {
	h := newRawHeap()
	a := h.alloc(64, 8)
	copy(h.bytes(a, 64), "stale")
	h.free(a)
	const other = 64 << 10
	freed := uint64(0)
	for freed+other+quarantineCharge < quarantineBytes {
		p := h.alloc(other, 8)
		h.free(p)
		freed += other + quarantineCharge
		if q := h.alloc(64, 8); q == a {
			t.Fatalf("reused after %d bytes were freed", freed)
		} else if !h.free(q) {
			t.Fatal("free")
		}
		freed += 64 + quarantineCharge
	}
	if string(h.bytes(a, 5)) != "stale" {
		t.Fatal("a quarantined block's bytes changed")
	}
	for freed < quarantineBytes {
		h.free(h.alloc(other, 8))
		freed += other + quarantineCharge
	}
	if q := h.alloc(64, 8); q != a {
		t.Fatalf("after %d bytes were freed the block is at %#x, not %#x again", freed, q, a)
	}
	for i, x := range h.bytes(a, 64) {
		if x != 0 {
			t.Fatalf("byte %d of a reused block is %d", i, x)
		}
	}
	if !h.is(a, 64, 8) {
		t.Fatal("the reused block is not live")
	}
}

// A program that frees what it allocates holds bounded memory: the
// quarantine and what is live, not everything it ever allocated.
func TestRawHeapBounded(t *testing.T) {
	h := newRawHeap()
	for i := 0; i < 80000; i++ {
		p := h.alloc(16<<10, 8)
		h.bytes(p, 8)[0] = 1
		if !h.free(p) {
			t.Fatal("free")
		}
	}
	if n := len(*h.chunks.Load()); n > quarantineBytes/chunkSize+2 {
		t.Fatalf("%d chunks after 80000 blocks of 16 KiB, each freed at once", n)
	}
	// large blocks: their chunks are unmapped
	for i := 0; i < 2000; i++ {
		p := h.alloc(1<<20, 8)
		if p == 0 || !h.free(p) {
			t.Fatal("large block")
		}
	}
	if n := len(*h.chunks.Load()); n > 2*quarantineBytes/chunkSize+4 {
		t.Fatalf("%d chunks after 2000 large blocks, each freed at once", n)
	}
	if len(h.ranges) != 1 {
		t.Fatalf("%d address ranges of unmapped blocks: adjacent ranges are merged", len(h.ranges))
	}
	if len(h.quar)-h.qhead > 2*quarantineBytes/chunkSize || cap(h.quar) > 1<<16 {
		t.Fatalf("quarantine of %d entries (capacity %d)", len(h.quar)-h.qhead, cap(h.quar))
	}
}

// A large block has a chunk of its own: it reads its own bytes while it is
// quarantined, and is unmapped after, at an address that is not given out
// again while there are others.
func TestRawHeapLarge(t *testing.T) {
	it := New(&ir.Module{}, "t.kern", nil, nil)
	h := it.heap
	const size = 3 << 20
	a := h.alloc(size, 4096)
	if a == 0 || a%4096 != 0 || h.bytes(a, size) == nil || h.bytes(a, size+16) != nil {
		t.Fatalf("large block at %#x", a)
	}
	b, _ := it.rawSpan(a+100, 8)
	copy(b, "big")
	h.free(a)
	if b, _ := it.rawSpan(a+100, 8); string(b[:3]) != "big" {
		t.Fatal("a quarantined large block's bytes changed")
	}
	seen := map[uint64]bool{a: true}
	for freed := 0; freed < quarantineBytes; freed += size {
		p := h.alloc(size, 8)
		if seen[p] {
			t.Fatalf("address %#x given out twice", p)
		}
		seen[p] = true
		h.free(p)
	}
	if h.bytes(a, 1) != nil {
		t.Fatal("a large block is still mapped after its quarantine")
	}
	// the thread's remembered chunk is not used once the chunk is unmapped
	if b, _ := it.rawSpan(a+100, 8); b != nil {
		t.Fatal("an unmapped large block read through the remembered chunk")
	}
	if it.rc.last.Load() != nil {
		t.Fatal("the unmapped chunk is still remembered")
	}
	if h.free(a) || h.is(a, size, 4096) {
		t.Fatal("the unmapped block is live")
	}
}

// When every address has been given out once, the addresses of unmapped
// large blocks are used again, and a request there is no room for fails.
func TestRawHeapAddressReuse(t *testing.T) {
	h := newRawHeap()
	h.limit = heapBase + 40<<20
	small := h.alloc(64, 8) // the bump chunk: 1 MiB of the 40
	live := map[uint64]bool{}
	for i := 0; i < 500; i++ {
		p := h.alloc(2<<20, 8)
		if p == 0 || p < heapBase || p+2<<20 > h.limit || live[p] {
			t.Fatalf("large block %d at %#x", i, p)
		}
		copy(h.bytes(p, 8), "live")
		live[p] = true
		if i%3 == 0 { // keep every third for a while
			continue
		}
		h.free(p)
		delete(live, p)
		if len(live) > 4 {
			for q := range live {
				if string(h.bytes(q, 4)) != "live" {
					t.Fatalf("live block %#x overwritten", q)
				}
				h.free(q)
				delete(live, q)
			}
		}
	}
	if h.alloc(64<<20, 8) != 0 {
		t.Fatal("a block larger than the address space was allocated")
	}
	if !h.is(small, 64, 8) {
		t.Fatal("the small block is not live")
	}
}

// Blocks of every alignment up to chunkSize are aligned, zero and disjoint
// from every live block, first time and when their addresses are reused.
func TestRawHeapAlignmentAndReuse(t *testing.T) {
	h := newRawHeap()
	r := rand.New(rand.NewSource(5))
	type block struct {
		addr, size uint64
		fill       byte
	}
	var live []block
	var total uint64
	check := func(b block) {
		for i, x := range h.bytes(b.addr, b.size) {
			if x != b.fill {
				t.Fatalf("block %#x of %d bytes: byte %d is %d, want %d", b.addr, b.size, i, x, b.fill)
			}
		}
	}
	for round := 0; round < 30000; round++ {
		if len(live) > 0 && (r.Intn(2) == 0 || total > 8<<20) {
			i := r.Intn(len(live))
			b := live[i]
			check(b)
			if !h.free(b.addr) {
				t.Fatalf("free of %#x", b.addr)
			}
			total -= b.size
			live[i] = live[len(live)-1]
			live = live[:len(live)-1]
			continue
		}
		align := uint64(1) << r.Intn(21)
		if r.Intn(4) != 0 {
			align = uint64(1) << r.Intn(5)
		}
		var size uint64
		switch r.Intn(10) {
		case 0:
			size = uint64(r.Intn(300 << 10))
		case 1, 2:
			size = uint64(r.Intn(8 << 10))
		default:
			size = uint64(r.Intn(200))
		}
		addr := h.alloc(size, align)
		if addr == 0 || addr%align != 0 {
			t.Fatalf("alloc(%d, %d) = %#x", size, align, addr)
		}
		b := block{addr, size, byte(round%251 + 1)}
		mem := h.bytes(addr, size)
		if size > 0 && mem == nil {
			t.Fatalf("alloc(%d, %d) = %#x is not mapped", size, align, addr)
		}
		for i, x := range mem {
			if x != 0 {
				t.Fatalf("alloc(%d, %d) = %#x: byte %d is %d", size, align, addr, i, x)
			}
			mem[i] = b.fill
		}
		live = append(live, b)
		total += size
	}
	for _, b := range live {
		check(b)
	}
}

// Threads allocate, write, read and free concurrently while chunks are
// added and removed under them (run with -race): each sees its own bytes.
func TestRawHeapConcurrent(t *testing.T) {
	main := New(&ir.Module{}, "t.kern", nil, nil)
	u64t := &resolve.Prim{Name: "u64"}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		it := main.child()
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(g)))
			type block struct{ addr, size, align uint64 }
			var live []block
			for round := 0; round < 4000; round++ {
				size := uint64(8 * (1 + r.Intn(64)))
				switch r.Intn(40) {
				case 0:
					size = 1 << 20 // a chunk of its own
				case 1:
					size = 64 << 10
				}
				b := block{it.heap.alloc(size, 8), size, 8}
				if b.addr == 0 {
					t.Error("alloc failed")
					return
				}
				tag := mkInt(tyU64, b.addr^uint64(g))
				it.encode(it.rawAtW(b.addr, u64t, noPos), u64t, tag)
				it.encode(it.rawAtW(b.addr+size-8, u64t, noPos), u64t, tag)
				live = append(live, b)
				if len(live) > 16 || round%7 == 0 {
					i := r.Intn(len(live))
					b := live[i]
					want := b.addr ^ uint64(g)
					if it.decode(it.rawAt(b.addr, u64t, noPos), u64t).(Int).Bits != want || it.decode(it.rawAt(b.addr+b.size-8, u64t, noPos), u64t).(Int).Bits != want {
						t.Errorf("thread %d: block %#x overwritten", g, b.addr)
						return
					}
					if round%2 == 0 {
						n, ok := it.heap.realloc(b.addr, b.size, b.size*2, b.align)
						if !ok || n == 0 || it.decode(it.rawAt(n, u64t, noPos), u64t).(Int).Bits != want {
							t.Errorf("thread %d: realloc of %#x", g, b.addr)
							return
						}
						b = block{n, b.size * 2, b.align}
					}
					if !it.heap.is(b.addr, b.size, b.align) || !it.heap.free(b.addr) {
						t.Errorf("thread %d: free of %#x", g, b.addr)
						return
					}
					live[i] = live[len(live)-1]
					live = live[:len(live)-1]
				}
			}
		}(g)
	}
	// mappings come and go meanwhile, as std/mem's do
	wg.Add(1)
	go func() {
		defer wg.Done()
		it := main.child()
		for i := 0; i < 2000; i++ {
			mem := make([]byte, 4096)
			mem[7] = byte(i)
			addr := it.heap.adopt(mem, true, 4096)
			b, ro := it.rawSpan(addr+7, 1)
			if b == nil || !ro || b[0] != byte(i) {
				t.Error("a mapping is not readable where it was adopted")
				return
			}
			if it.heap.release(addr) == nil || !it.heap.free(addr) {
				t.Error("release of a mapping")
				return
			}
			if b, _ := it.rawSpan(addr+7, 1); b != nil {
				t.Error("a released mapping is still addressable")
				return
			}
		}
	}()
	wg.Wait()
}

// A place in raw memory made on one thread and read on others uses the
// first thread's remembered chunk from all of them (run with -race).
func TestRawCacheShared(t *testing.T) {
	it := New(&ir.Module{}, "t.kern", nil, nil)
	u64t := &resolve.Prim{Name: "u64"}
	var refs []rawRef
	for i := 0; i < 64; i++ {
		addr := it.heap.alloc(200<<10, 8) // each in its own chunk
		r := rawRef{it: it, addr: addr, t: u64t}
		r.set(mkInt(tyU64, uint64(i)))
		refs = append(refs, r)
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < 2000; n++ {
				i := (n*7 + g) % len(refs)
				if got := refs[i].get().(Int).Bits; got != uint64(i) {
					t.Errorf("place %d reads %d", i, got)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// A library object has one handle however often it is stored, the handle
// is released when the object is destroyed, and a released, forged or
// never-issued handle is no object's.
func TestHandleTable(t *testing.T) {
	it := New(&ir.Module{}, "t.kern", nil, nil)
	h := it.heap
	s := &Shared{Val: mkInt(tyU64, 1), Count: 1}
	id := h.handle(s)
	if id == 0 || h.handle(s) != id || h.handleValue(id) != Value(s) {
		t.Fatalf("handle %d", id)
	}
	m := &Mutex{Val: mkInt(tyU64, 2)}
	if mid := h.handle(m); mid == id || h.handleValue(mid) != Value(m) {
		t.Fatal("two objects share a handle")
	}
	f, g := FuncVal{Name: "m.f"}, FuncVal{Name: "m.g"}
	fid, gid := h.handle(f), h.handle(g)
	if h.handle(FuncVal{Name: "m.f"}) != fid || fid == gid || h.handleValue(gid) != Value(g) {
		t.Fatal("a function has one handle")
	}
	for _, bad := range []uint64{0, 1 << 20, id | 1<<32, ^uint64(0), 1 << 32} {
		if h.handleValue(bad) != nil {
			t.Fatalf("handle %#x was never issued", bad)
		}
	}
	h.dropHandle(s)
	if h.handleValue(id) != nil || s.hid.Load() != 0 {
		t.Fatal("a released handle still names its object")
	}
	h.dropHandle(s) // nothing to release
	s2 := &Shared{Count: 1}
	id2 := h.handle(s2)
	if id2 == id || uint32(id2) != uint32(id) || h.handleValue(id2) != Value(s2) || h.handleValue(id) != nil {
		t.Fatalf("the entry of handle %#x was issued again as %#x", id, id2)
	}
	// the allocators have one handle between them, which outlives each
	a1, a2 := &SysAlloc{}, &SysAlloc{}
	aid := h.handle(a1)
	h.dropHandle(a1)
	if _, ok := h.handleValue(aid).(*SysAlloc); !ok || h.handle(a2) != aid {
		t.Fatal("the allocators' handle")
	}
	// a value that is no library object keeps the entry it is given
	odd := &Struct{}
	if oid := h.handle(odd); h.handleValue(oid) != Value(odd) {
		t.Fatal("a placeholder does not round-trip")
	}
	// destroying an object releases its handle: Shared with its last reference
	n := len(h.handles)
	sharedT := &resolve.Named{Decl: &resolve.TypeDecl{Module: "core", Name: "Shared", NoCopy: true}, Args: []resolve.Type{&resolve.Prim{Name: "u64"}}}
	for i := 0; i < 10000; i++ {
		o := &Shared{Val: mkInt(tyU64, uint64(i)), Count: 2}
		b := it.rawAt(h.alloc(8, 8), sharedT, noPos)
		it.encode(b, sharedT, o)
		it.encode(b, sharedT, o)
		if it.decode(b, sharedT) != Value(o) {
			t.Fatal("a stored object does not decode to itself")
		}
		it.destroy(o, sharedT)
		if it.decode(b, sharedT) != Value(o) {
			t.Fatal("an object with a reference left lost its handle")
		}
		it.destroy(o, sharedT)
		if _, dead := it.decode(b, sharedT).(*Shared); dead {
			t.Fatal("a destroyed object's handle still decodes to it")
		}
	}
	if len(h.handles) > n+1 {
		t.Fatalf("the table grew from %d to %d entries for one live object at a time", n, len(h.handles))
	}
}

// Objects are stored and released from several threads (run with -race).
func TestHandleTableConcurrent(t *testing.T) {
	h := newRawHeap()
	shared := &Shared{Count: 1}
	var wg sync.WaitGroup
	ids := make([]uint64, 8)
	for g := range ids {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			ids[g] = h.handle(shared)
			for i := 0; i < 5000; i++ {
				m := &Mutex{}
				id := h.handle(m)
				if h.handleValue(id) != Value(m) || h.handleValue(ids[g]) != Value(shared) {
					t.Error("a live handle does not name its object")
					return
				}
				h.dropHandle(m)
				if h.handleValue(id) != nil {
					t.Error("a released handle names an object")
					return
				}
			}
		}(g)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatal("one object was given two handles")
		}
	}
	if len(h.handles) > 9 {
		t.Fatalf("%d entries for 9 objects alive at once", len(h.handles))
	}
}

// Whole programs: memory the program frees is used again, handles are
// released with their objects, and the output is what it was.
func TestRawHeapPrograms(t *testing.T) {
	for _, c := range []struct {
		name, src, want string
		check           func(t *testing.T, h *rawHeap)
	}{
		{"churn", `module churn
func main() -> i32 {
    total := 0u64
    for r in 0u64..6000u64 {
        v := Vec[u64].with_capacity(2048usize)
        v.push(r)
        total = total + v.get(0usize)
    }
    print_u64(total)
    println()
    return 0i32
}
`, "17997000\n", func(t *testing.T, h *rawHeap) {
			// 6000 blocks of 16 KiB are 94 MiB
			if n := len(*h.chunks.Load()); n > quarantineBytes/chunkSize+2 {
				t.Errorf("%d chunks", n)
			}
		}},
		{"handles", `module handles
func main() -> i32 {
    v := Vec[Shared[u64]].new()
    keep := Vec[Shared[u64]].new()
    total := 0u64
    for r in 0u64..20000u64 {
        s := Shared[u64].new(r)
        if r % 1000u64 == 0u64 { keep.push(s.clone()) }
        v.push(move s)
        match v.pop() {
            Some(s) => { total = wrapping_add(total, s.get()) }
            None => {}
        }
    }
    for i in 0usize..keep.len() { total = wrapping_add(total, keep.get(i).get()) }
    print_u64(total)
    println()
    return 0i32
}
`, "200180000\n", func(t *testing.T, h *rawHeap) {
			// 20 objects are kept; each of the others had a handle while it lived
			if n := len(h.handles); n > 24 {
				t.Errorf("%d handle entries", n)
			}
		}},
	} {
		path := filepath.Join(t.TempDir(), c.name+".kern")
		if err := os.WriteFile(path, []byte(c.src), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := source.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, level := range []string{"debug", "release"} {
			mod, why := lowerFor([]*source.File{f}, level)
			if mod == nil {
				t.Fatalf("%s: %s", c.name, why)
			}
			var out, errb bytes.Buffer
			it := New(mod, path, &out, &errb)
			if status := it.Run(); status != 0 || out.String() != c.want {
				t.Fatalf("%s %s: status %d, output %q, want %q\n%s", c.name, level, status, out.String(), c.want, errb.String())
			}
			c.check(t, it.heap)
		}
	}
}
