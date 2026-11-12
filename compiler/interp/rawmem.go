package interp

import (
	"encoding/binary"
	"math"
	"math/bits"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// rawHeap is the interpreter's byte-addressed memory for raw pointers (PLAN.md
// 2.3): allocations live at addresses above heapBase, values are stored in
// their §4.1 layout, and library objects and function values stored into
// raw memory are kept in a handle table and represented by their handle.
//
// Memory is laid out in chunks. A block of at most smallMax bytes is one of
// a size class and is carved from a chunk of chunkSize bytes shared with
// other blocks; a larger block has a chunk of its own. A freed block is
// first quarantined (see quarantineBytes): its bytes stay mapped and
// unchanged, and its address is not given out again, until that much memory
// has been freed after it. A small block then goes to its class's free
// list, to be cleared and given out again; a large block's chunk is
// unmapped, so that a later access traps, and its memory is the collector's.
//
// What a program can do wrong and what becomes of it:
//   - free or realloc of an address that is not the start of a live block,
//     or with another size or alignment than it was allocated with (a
//     double free and a free of an address inside a block included): is
//     reports false and the caller traps with kind explicit;
//   - an access to an address no chunk maps, or running past the end of the
//     chunk it starts in: span returns nil and the caller traps;
//   - an access to a freed block: the block's own last bytes while it is
//     quarantined; after that a small block's bytes are those of whatever
//     was allocated there since, and a large block's are a trap;
//   - an access past the end of a block but inside its chunk is not caught
//     (a large block's chunk ends where the block does, to within minBlock).
type rawHeap struct {
	// chunks is every mapped chunk, the file mappings among them, in
	// address order. A published slice is never changed: adding or
	// removing a chunk publishes a new one, so a reader takes no lock.
	chunks atomic.Pointer[[]*heapChunk]

	mu     sync.Mutex          // guards what follows, and the publishing of chunks
	allocs map[uint64]rawBlock // address -> the live allocation there
	bump   *heapChunk          // the chunk new small blocks are carved from
	next   uint64              // the lowest address never given to a chunk
	limit  uint64              // the end of the addresses chunks may have
	// ranges are the address ranges of unmapped large blocks, in address
	// order and merged where they touch. They are used only when every
	// address below limit has been given out once, so that a stale pointer
	// to a large block traps for as long as that can be arranged.
	ranges []addrRange
	// freeBlocks holds the addresses of the reusable blocks of each size class
	// with the allocator's own alignment (minBlock); freeAligned those of
	// blocks allocated with a larger one, by class and alignment.
	freeBlocks  [numClasses][]uint64
	freeAligned map[uint16][]uint64
	// quar are the quarantined blocks, oldest first from qhead, each an
	// address with its free-list key above bit 48; qbytes is what they
	// are charged
	quar   []uint64
	qhead  int
	qbytes uint64
	// mapNext is the address the next file mapping (std/mem) gets: memory
	// the system mapped, given addresses of the interpreter's above
	// mapBase, and gone from chunks when unmapped, so that a later access
	// is a trap and not a fault
	mapNext uint64

	hmu     sync.RWMutex // guards the handle table
	handles []handleEntry
	hfree   []uint32          // the released entries, to be issued again
	funcs   map[string]uint64 // a function's handle, by its name
	sysHid  atomic.Uint64     // the allocators' handle (handleSlot)
}

// heapChunk is one stable backing array; allocations never span chunks.
// base, mem and ro are set before the chunk is published and never change.
// ro marks a file mapping that was not made writable: the system would
// fault a write to it.
type heapChunk struct {
	base uint64
	mem  []byte
	used uint64 // of the bump chunk: where its unallocated bytes start (under mu)
	ro   bool
	// dead is set when the chunk leaves chunks: a thread's remembered
	// chunk (rawCache) is not used after that
	dead atomic.Bool
}

type addrRange struct{ base, size uint64 }

const (
	heapBase     = 0x1_0000_0000
	mapBase      = 0x4000_0000_0000
	chunkSize    = 1 << 20
	maxAllocSize = 1 << 40 // larger requests fail (the interpreter's address space)

	// minBlock is the size of the smallest block and the alignment of
	// every block that asked for no more; smallMax is the largest size
	// with a size class. The classes are every multiple of 16 up to 128,
	// then four to each doubling (160, 192, 224, 256, 320, ...), so that a
	// block is less than a quarter larger than what was asked for.
	minBlock   = 16
	smallMax   = 128 << 10
	numClasses = 48
	largeClass = 63 // the key of a large block in the quarantine

	// quarantineBytes is how much memory must be freed after a block
	// before its address can be given out again (small blocks) or stops
	// being mapped (large ones). A block is charged its class's size, or
	// its own when it is large, and quarantineCharge for the queue's and
	// the free list's entries. What it costs: a program that frees holds
	// up to this much dead memory, and the last block it freed, on top of
	// what it has live; one that frees less than this in all holds what
	// it did when nothing was reused.
	quarantineBytes  = 16 << 20
	quarantineCharge = 16
)

func newRawHeap() *rawHeap {
	h := &rawHeap{allocs: map[uint64]rawBlock{}, next: heapBase, limit: mapBase}
	h.chunks.Store(&[]*heapChunk{})
	return h
}

// rawBlock is a live allocation as its caller asked for it: free and
// realloc must name the same size and alignment (specs/runtime RF1).
type rawBlock struct{ size, align uint64 }

// classOf is the size class of a request of n bytes, 1 to smallMax: its
// index and the size of its blocks.
func classOf(n uint64) (int, uint64) {
	if n <= 128 {
		idx := int((n+15)>>4) - 1
		return idx, uint64(idx+1) << 4
	}
	lg := bits.Len64(n - 1) // 2^(lg-1) < n <= 2^lg
	shift := uint(lg - 3)
	size := (n + 1<<shift - 1) >> shift << shift
	return 8 + (lg-8)*4 + int(size>>shift) - 5, size
}

// classSize is the size of the blocks of class idx.
func classSize(idx int) uint64 {
	if idx < 8 {
		return uint64(idx+1) << 4
	}
	k := idx - 8
	return uint64(5+k%4) << uint(5+k/4)
}

// blockKey is the free list a block of n bytes at align belongs to: its
// size class, with the alignment's logarithm above bit 6 when the
// alignment is more than every block has; largeClass for a large block.
func blockKey(n, align uint64) uint16 {
	if n > smallMax {
		return largeClass
	}
	idx, _ := classOf(n)
	if align <= minBlock {
		return uint16(idx)
	}
	return uint16(idx) | uint16(bits.TrailingZeros64(align))<<6
}

// is reports whether addr is a live allocation of this size and alignment.
func (h *rawHeap) is(addr, size, align uint64) bool {
	h.mu.Lock()
	b, ok := h.allocs[addr]
	h.mu.Unlock()
	return ok && b.size == size && b.align == align
}

// alloc reserves size bytes at an address that is a multiple of align (a
// power of two), or returns 0 when the request cannot be honoured
// (allocation failure). The bytes are zero: a chunk starts zeroed and a
// reused block is cleared, so that what a program reads from memory it did
// not write does not depend on what was freed before.
func (h *rawHeap) alloc(size, align uint64) uint64 {
	if size > maxAllocSize || align > chunkSize || align == 0 || align&(align-1) != 0 {
		return 0
	}
	n := size
	if n == 0 {
		n = 1 // zero-size allocations are unique
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var addr uint64
	if n > smallMax {
		addr = h.allocLarge(n)
	} else {
		addr = h.allocSmall(n, align)
	}
	if addr != 0 {
		h.allocs[addr] = rawBlock{size, align}
	}
	return addr
}

// allocSmall takes a block of n's class from its free list, or carves one
// from the bump chunk, starting a new chunk when it does not fit.
func (h *rawHeap) allocSmall(n, align uint64) uint64 {
	_, size := classOf(n)
	if addr, ok := h.popFree(blockKey(n, align)); ok {
		c := h.chunkOf(addr)
		off := addr - c.base
		clear(c.mem[off : off+size])
		return addr
	}
	if align < minBlock {
		align = minBlock
	}
	c := h.bump
	var off uint64
	if c != nil {
		off = (c.used + align - 1) &^ (align - 1)
	}
	if c == nil || off+size > uint64(len(c.mem)) {
		base := h.reserve(chunkSize)
		if base == 0 {
			return 0
		}
		c = &heapChunk{base: base, mem: make([]byte, chunkSize)}
		h.addChunk(c)
		h.bump, off = c, 0
	}
	c.used = off + size
	return c.base + off // a chunk's base is a multiple of chunkSize, the largest alignment
}

// allocLarge gives a block of more than smallMax bytes a chunk of its own,
// so that it is unmapped whole when it leaves the quarantine.
func (h *rawHeap) allocLarge(n uint64) uint64 {
	size := (n + minBlock - 1) &^ (minBlock - 1)
	base := h.reserve((size + chunkSize - 1) &^ (chunkSize - 1))
	if base == 0 {
		return 0
	}
	h.addChunk(&heapChunk{base: base, mem: make([]byte, size)})
	return base
}

// reserve returns span bytes of addresses (a multiple of chunkSize) for a
// new chunk, or 0 when there are none: addresses never used while there
// are any, then those of unmapped large blocks.
func (h *rawHeap) reserve(span uint64) uint64 {
	if span <= h.limit-h.next {
		base := h.next
		h.next += span
		return base
	}
	for i, r := range h.ranges {
		if r.size < span {
			continue
		}
		if r.size == span {
			h.ranges = append(h.ranges[:i], h.ranges[i+1:]...)
		} else {
			h.ranges[i] = addrRange{r.base + span, r.size - span}
		}
		return r.base
	}
	return 0
}

// unreserve records that the addresses of an unmapped chunk are free.
func (h *rawHeap) unreserve(base, span uint64) {
	i := sort.Search(len(h.ranges), func(i int) bool { return h.ranges[i].base > base })
	if i > 0 && h.ranges[i-1].base+h.ranges[i-1].size == base {
		i--
		h.ranges[i].size += span
	} else {
		h.ranges = append(h.ranges, addrRange{})
		copy(h.ranges[i+1:], h.ranges[i:])
		h.ranges[i] = addrRange{base, span}
	}
	if i+1 < len(h.ranges) && h.ranges[i].base+h.ranges[i].size == h.ranges[i+1].base {
		h.ranges[i].size += h.ranges[i+1].size
		h.ranges = append(h.ranges[:i+1], h.ranges[i+2:]...)
	}
}

func (h *rawHeap) popFree(key uint16) (uint64, bool) {
	if key < numClasses {
		l := h.freeBlocks[key]
		if len(l) == 0 {
			return 0, false
		}
		h.freeBlocks[key] = l[:len(l)-1]
		return l[len(l)-1], true
	}
	l := h.freeAligned[key]
	if len(l) == 0 {
		return 0, false
	}
	h.freeAligned[key] = l[:len(l)-1]
	return l[len(l)-1], true
}

func (h *rawHeap) pushFree(key uint16, addr uint64) {
	if key < numClasses {
		h.freeBlocks[key] = append(h.freeBlocks[key], addr)
		return
	}
	if h.freeAligned == nil {
		h.freeAligned = map[uint16][]uint64{}
	}
	h.freeAligned[key] = append(h.freeAligned[key], addr)
}

// free releases an allocation; it reports whether addr was live. The block
// is quarantined: nothing of it changes yet but that it is not live.
func (h *rawHeap) free(addr uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.allocs[addr]
	if !ok {
		return false
	}
	delete(h.allocs, addr)
	if addr >= mapBase {
		return true // a file mapping: its memory left with release
	}
	n := b.size
	if n == 0 {
		n = 1
	}
	e := addr | uint64(blockKey(n, b.align))<<48
	h.quar = append(h.quar, e)
	h.qbytes += h.charge(e)
	// the oldest block leaves when quarantineBytes have been freed after it
	for {
		oldest := h.quar[h.qhead]
		c := h.charge(oldest)
		if h.qbytes-c < quarantineBytes {
			break
		}
		h.qbytes -= c
		h.qhead++
		h.retire(oldest)
	}
	if h.qhead >= 1024 && h.qhead*2 >= len(h.quar) {
		h.quar = h.quar[:copy(h.quar, h.quar[h.qhead:])]
		h.qhead = 0
	}
	return true
}

// charge is what a quarantined block counts for.
func (h *rawHeap) charge(e uint64) uint64 {
	key := uint16(e >> 48)
	if key == largeClass {
		return uint64(len(h.chunkOf(e&(1<<48-1)).mem)) + quarantineCharge
	}
	return classSize(int(key&63)) + quarantineCharge
}

// retire ends a block's quarantine: a small block may be allocated again,
// a large block's chunk is unmapped.
func (h *rawHeap) retire(e uint64) {
	addr, key := e&(1<<48-1), uint16(e>>48)
	if key != largeClass {
		h.pushFree(key, addr)
		return
	}
	c := h.chunkOf(addr)
	h.removeChunk(c)
	h.unreserve(c.base, (uint64(len(c.mem))+chunkSize-1)&^(chunkSize-1))
}

// realloc moves a live allocation to a new block of size bytes at align,
// keeping the common prefix; it returns 0 (and leaves the old block) when
// the new block cannot be obtained, and false when addr is not live. The
// block always moves, so the old address is dead, and quarantined, at once.
func (h *rawHeap) realloc(addr, oldSize, size, align uint64) (uint64, bool) {
	h.mu.Lock()
	live, ok := h.allocs[addr]
	h.mu.Unlock()
	if !ok {
		return 0, false
	}
	if live.size < oldSize {
		oldSize = live.size
	}
	n := h.alloc(size, align)
	if n == 0 {
		return 0, true
	}
	keep := oldSize
	if size < keep {
		keep = size
	}
	if keep > 0 {
		copy(h.bytes(n, keep), h.bytes(addr, keep))
	}
	h.free(addr)
	return n, true
}

// zero clears the n bytes at addr (alloc_zeroed: alloc's bytes are zero
// already, but the guarantee belongs to the call).
func (h *rawHeap) zero(addr, n uint64) {
	if n > 0 {
		clear(h.bytes(addr, n))
	}
}

// chunkOf is the chunk that maps addr, or nil: a binary search of the
// published chunks, with no lock.
func (h *rawHeap) chunkOf(addr uint64) *heapChunk {
	list := *h.chunks.Load()
	lo, hi := 0, len(list)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if list[mid].base <= addr {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return nil
	}
	if c := list[lo-1]; addr-c.base < uint64(len(c.mem)) {
		return c
	}
	return nil
}

// addChunk and removeChunk publish the chunks with c added or removed
// (under mu).
func (h *rawHeap) addChunk(c *heapChunk) {
	old := *h.chunks.Load()
	i := sort.Search(len(old), func(i int) bool { return old[i].base > c.base })
	list := make([]*heapChunk, len(old)+1)
	copy(list, old[:i])
	list[i] = c
	copy(list[i+1:], old[i:])
	h.chunks.Store(&list)
}

func (h *rawHeap) removeChunk(c *heapChunk) {
	old := *h.chunks.Load()
	i := sort.Search(len(old), func(i int) bool { return old[i].base >= c.base })
	list := make([]*heapChunk, 0, len(old)-1)
	list = append(append(list, old[:i]...), old[i+1:]...)
	h.chunks.Store(&list)
	c.dead.Store(true)
}

// bytes returns the n bytes at addr, or nil when the range is not mapped.
// A chunk's memory never moves, so the slice stays valid; concurrent access
// to one allocation is the program's data race.
func (h *rawHeap) bytes(addr, n uint64) []byte {
	b, _ := h.span(addr, n)
	return b
}

// span is bytes, and whether the bytes may only be read.
func (h *rawHeap) span(addr, n uint64) ([]byte, bool) {
	c := h.chunkOf(addr)
	if c == nil {
		return nil, false
	}
	off := addr - c.base
	if n > uint64(len(c.mem))-off {
		return nil, false
	}
	return c.mem[off : off+n], c.ro
}

// rawCache is what one thread remembers of the raw heap: the chunk of its
// last access, which the next is most often in. It is atomic because a
// place in raw memory names the interpreter of the thread that made it
// (rawRef) and may be read on another (a view passed to a scoped thread).
type rawCache struct{ last atomic.Pointer[heapChunk] }

// rawSpan is the heap's span through this thread's remembered chunk.
func (it *Interp) rawSpan(addr, n uint64) ([]byte, bool) {
	c := it.rc.last.Load()
	if c == nil || addr-c.base >= uint64(len(c.mem)) || c.dead.Load() {
		c = it.heap.chunkOf(addr)
		it.rc.last.Store(c) // nil too: an unmapped chunk is not kept from the collector
		if c == nil {
			return nil, false
		}
	}
	off := addr - c.base
	if n > uint64(len(c.mem))-off {
		return nil, false
	}
	return c.mem[off : off+n], c.ro
}

// adopt gives memory the system mapped an address of the interpreter's, a
// multiple of align, and records it as a live block of its length.
func (h *rawHeap) adopt(mem []byte, ro bool, align uint64) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mapNext == 0 {
		h.mapNext = mapBase
	}
	base := (h.mapNext + align - 1) / align * align
	h.mapNext = base + uint64(len(mem)) + align // a gap: running off the end of one mapping is not the next
	h.addChunk(&heapChunk{base: base, mem: mem, ro: ro})
	h.allocs[base] = rawBlock{uint64(len(mem)), align}
	return base
}

// release forgets the mapping at addr and returns its memory for the
// caller to unmap, or nil when there is none.
func (h *rawHeap) release(addr uint64) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	if addr < mapBase {
		return nil
	}
	c := h.chunkOf(addr)
	if c == nil || c.base != addr {
		return nil
	}
	h.removeChunk(c)
	return c.mem
}

// handleEntry is one entry of the handle table. gen counts the times the
// entry has been released: a handle is its entry's number, from 1, with
// the generation it was issued in above bit 32, so that the handle of a
// released entry is not the handle of what the entry holds next.
type handleEntry struct {
	v   Value
	gen uint32
}

// handleSlot is where a library object keeps its handle, nil for a value
// that is not one. The allocators are one value (SysAlloc has no state),
// so they have one handle between them, which is never released.
func (h *rawHeap) handleSlot(v Value) *atomic.Uint64 {
	switch x := v.(type) {
	case *Shared:
		return &x.hid
	case *Mutex:
		return &x.hid
	case *Condvar:
		return &x.hid
	case *Scope:
		return &x.hid
	case *Thread:
		return &x.hid
	case *Join:
		return &x.hid
	case *SysAlloc:
		return &h.sysHid
	}
	return nil
}

// handle is the handle that stands for v in raw memory. A library object
// has one handle from its first store into raw memory until it is
// destroyed (dropHandle), however often it is stored, and a function has
// one for the whole run: the table holds the objects raw memory can name,
// not one entry for every store. Any other value (the zero a bad handle
// decodes to, stored back) gets an entry of its own that is never released.
func (h *rawHeap) handle(v Value) uint64 {
	if f, ok := v.(FuncVal); ok {
		h.hmu.RLock()
		id, ok := h.funcs[f.Name]
		h.hmu.RUnlock()
		if ok {
			return id
		}
		h.hmu.Lock()
		defer h.hmu.Unlock()
		if id, ok := h.funcs[f.Name]; ok {
			return id
		}
		if h.funcs == nil {
			h.funcs = map[string]uint64{}
		}
		id = h.newHandle(v)
		h.funcs[f.Name] = id
		return id
	}
	slot := h.handleSlot(v)
	if slot == nil {
		h.hmu.Lock()
		defer h.hmu.Unlock()
		return h.newHandle(v)
	}
	if id := slot.Load(); id != 0 {
		return id
	}
	h.hmu.Lock()
	defer h.hmu.Unlock()
	if id := slot.Load(); id != 0 {
		return id // stored by another thread meanwhile
	}
	id := h.newHandle(v)
	slot.Store(id)
	return id
}

// newHandle puts v in a released entry, or a new one (under hmu).
func (h *rawHeap) newHandle(v Value) uint64 {
	if n := len(h.hfree); n > 0 {
		i := h.hfree[n-1]
		h.hfree = h.hfree[:n-1]
		h.handles[i].v = v
		return uint64(h.handles[i].gen)<<32 | uint64(i+1)
	}
	h.handles = append(h.handles, handleEntry{v: v})
	return uint64(len(h.handles))
}

// dropHandle releases the handle of a library object that has been
// destroyed or consumed: nothing owns the object, so no live value in raw
// memory names it. The handle is then no object's: one still lying in dead
// bytes decodes as a handle that was never issued does.
func (h *rawHeap) dropHandle(v Value) {
	slot := h.handleSlot(v)
	if slot == nil || slot == &h.sysHid || slot.Load() == 0 {
		return
	}
	h.hmu.Lock()
	defer h.hmu.Unlock()
	id := slot.Swap(0)
	i := uint32(id) - 1
	if id == 0 || uint64(i) >= uint64(len(h.handles)) || h.handles[i].gen != uint32(id>>32) {
		return
	}
	e := &h.handles[i]
	e.v = nil
	e.gen++
	if e.gen != 0 { // an entry whose generations are used up is retired
		h.hfree = append(h.hfree, i)
	}
}

// handleValue is the value a handle stands for, or nil for a handle that
// was never issued or has been released.
func (h *rawHeap) handleValue(id uint64) Value {
	i := uint32(id) - 1
	h.hmu.RLock()
	var v Value
	if uint64(i) < uint64(len(h.handles)) && h.handles[i].gen == uint32(id>>32) {
		v = h.handles[i].v
	}
	h.hmu.RUnlock()
	return v
}

// rawAt returns the bytes of a value of type t at addr, trapping with kind
// explicit when the address is not mapped (a raw-pointer obligation).
func (it *Interp) rawAt(addr uint64, t resolve.Type, pos ast.Pos) []byte {
	n := uint64(layout.Of(t).Size)
	b, _ := it.rawSpan(addr, n)
	if b == nil && n > 0 {
		it.trap("explicit", pos)
	}
	return b
}

// rawAtW is rawAt for a write: memory that may only be read (a file
// mapping that was not made writable) traps with kind unreachable, where
// the system would fault and the runtime report that kind.
func (it *Interp) rawAtW(addr uint64, t resolve.Type, pos ast.Pos) []byte {
	n := uint64(layout.Of(t).Size)
	b, ro := it.rawSpan(addr, n)
	if b == nil && n > 0 {
		it.trap("explicit", pos)
	}
	if ro && n > 0 {
		it.trap("unreachable", pos) // the system's fault, which the runtime reports as this kind
	}
	return b
}

// encode writes v of type t into b in its §4.1 layout.
func (it *Interp) encode(b []byte, t resolve.Type, v Value) {
	switch x := t.(type) {
	case *resolve.Prim:
		switch x.Name {
		case "bool":
			if bool(v.(Bool)) {
				b[0] = 1
			} else {
				b[0] = 0
			}
		case "f32":
			binary.LittleEndian.PutUint32(b, math.Float32bits(float32(v.(Float).V)))
		case "f64":
			binary.LittleEndian.PutUint64(b, math.Float64bits(v.(Float).V))
		default:
			putInt(b, v.(Int).Bits, layout.Of(t).Size)
		}
	case *resolve.RawPtr:
		putInt(b, v.(Int).Bits, 8)
	case *resolve.Func:
		putInt(b, it.heap.handle(v), 8)
	case *resolve.Unit:
	case *resolve.Array:
		es := layout.Of(x.Elem).Size
		for i, e := range v.(*Array).Elems {
			it.encode(b[i*es:], x.Elem, e)
		}
	case *resolve.Tuple:
		li := layout.Of(t)
		for i, e := range v.(*Struct).Fields {
			it.encode(b[li.Fields[i]:], x.Elems[i], e)
		}
	case *resolve.Named:
		if layout.Opaque(x) {
			putInt(b, it.heap.handle(v), 8)
			return
		}
		args := typeArgs(x)
		switch x.Decl.Kind {
		case resolve.KindStruct:
			li := layout.Of(t)
			for i, f := range x.Decl.Fields {
				it.encode(b[li.Fields[i]:], resolve.Subst(f.Type, args), v.(*Struct).Fields[i])
			}
		case resolve.KindUnion:
			u := v.(*Union)
			li := layout.Of(t)
			putInt(b, uint64(u.Tag), li.TagSize)
			vd := x.Decl.Variants[u.Tag]
			offs := layout.VariantLayout(x, vd)
			fts := resolve.VariantFieldTypes(x, vd)
			for i, f := range u.Fields {
				it.encode(b[offs[i]:], fts[i], f)
			}
		}
	}
}

// decode reads a value of type t from b.
func (it *Interp) decode(b []byte, t resolve.Type) Value {
	switch x := t.(type) {
	case *resolve.Prim:
		switch x.Name {
		case "bool":
			return Bool(b[0] != 0)
		case "f32":
			return mkFloat("f32", float64(math.Float32frombits(binary.LittleEndian.Uint32(b))))
		case "f64":
			return mkFloat("f64", math.Float64frombits(binary.LittleEndian.Uint64(b)))
		}
		ty := tyOf(x.Name)
		return mkInt(ty, getInt(b, int(width(ty)/8)))
	case *resolve.RawPtr:
		return mkInt(tyUsize, getInt(b, 8))
	case *resolve.Func:
		if v := it.heap.handleValue(getInt(b, 8)); v != nil {
			return v
		}
		return FuncVal{}
	case *resolve.Unit:
		return Unit{}
	case *resolve.Array:
		es := layout.Of(x.Elem).Size
		a := &Array{Elems: make([]Value, x.Len)}
		for i := range a.Elems {
			a.Elems[i] = it.decode(b[i*es:], x.Elem)
		}
		return a
	case *resolve.Tuple:
		li := layout.Of(t)
		s := newStruct(len(x.Elems))
		for i, et := range x.Elems {
			s.Fields[i] = it.decode(b[li.Fields[i]:], et)
		}
		return s
	case *resolve.Named:
		if layout.Opaque(x) {
			if v := it.heap.handleValue(getInt(b, 8)); v != nil {
				return v
			}
			return zeroOf(t)
		}
		args := typeArgs(x)
		switch x.Decl.Kind {
		case resolve.KindStruct:
			li := layout.Of(t)
			s := newStruct(len(x.Decl.Fields))
			for i, f := range x.Decl.Fields {
				s.Fields[i] = it.decode(b[li.Fields[i]:], resolve.Subst(f.Type, args))
			}
			return s
		case resolve.KindUnion:
			li := layout.Of(t)
			tag := int(getInt(b, li.TagSize))
			if tag >= len(x.Decl.Variants) {
				tag = 0
			}
			vd := x.Decl.Variants[tag]
			offs := layout.VariantLayout(x, vd)
			fts := resolve.VariantFieldTypes(x, vd)
			u := newUnion(tag, len(vd.Fields))
			for i := range vd.Fields {
				u.Fields[i] = it.decode(b[offs[i]:], fts[i])
			}
			return u
		}
	}
	return zeroOf(t)
}

// putInt and getInt store and load a little-endian integer of size bytes:
// one load or store for the integer widths (a byte loop cost a quarter of
// the interpreter's time on raw-memory programs), a loop for any other size.
func putInt(b []byte, v uint64, size int) {
	switch size {
	case 8:
		binary.LittleEndian.PutUint64(b, v)
	case 4:
		binary.LittleEndian.PutUint32(b, uint32(v))
	case 2:
		binary.LittleEndian.PutUint16(b, uint16(v))
	case 1:
		b[0] = byte(v)
	default:
		for i := 0; i < size; i++ {
			b[i] = byte(v >> (8 * i))
		}
	}
}

func getInt(b []byte, size int) uint64 {
	switch size {
	case 8:
		return binary.LittleEndian.Uint64(b)
	case 4:
		return uint64(binary.LittleEndian.Uint32(b))
	case 2:
		return uint64(binary.LittleEndian.Uint16(b))
	case 1:
		return uint64(b[0])
	}
	var v uint64
	for i := 0; i < size; i++ {
		v |= uint64(b[i]) << (8 * i)
	}
	return v
}
