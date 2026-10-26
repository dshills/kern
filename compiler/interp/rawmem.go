package interp

import (
	"encoding/binary"
	"math"
	"sync"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// rawHeap is the interpreter's byte-addressed memory for raw pointers (PLAN.md
// 2.3): allocations live at addresses above heapBase in one growing arena,
// values are stored in their §4.1 layout, and library objects and function
// values stored into raw memory are kept in a handle table and represented
// by their handle number.
type rawHeap struct {
	mu      sync.Mutex
	chunks  []*heapChunk      // never moved once created, so slices stay valid
	allocs  map[uint64]uint64 // address -> size of live allocations
	handles []Value
}

// heapChunk is one stable backing array; allocations never span chunks.
type heapChunk struct {
	base uint64
	mem  []byte
	used uint64
}

const (
	heapBase     = 0x1_0000_0000
	chunkSize    = 1 << 20
	maxAllocSize = 1 << 40 // larger requests fail (the interpreter's address space)
)

func newRawHeap() *rawHeap { return &rawHeap{allocs: map[uint64]uint64{}} }

// alloc reserves size bytes at an address that is a multiple of align, or
// returns 0 when the request cannot be honoured (allocation failure).
func (h *rawHeap) alloc(size, align uint64) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if size > maxAllocSize || align > chunkSize || align == 0 {
		return 0
	}
	n := size
	if n == 0 {
		n = 1 // zero-size allocations are unique
	}
	var c *heapChunk
	var off uint64
	if len(h.chunks) > 0 {
		last := h.chunks[len(h.chunks)-1]
		off = (last.used + align - 1) / align * align
		if off+n <= uint64(len(last.mem)) {
			c = last
		}
	}
	if c == nil {
		sz := uint64(chunkSize)
		if n+align > sz {
			sz = n + align
		}
		base := uint64(heapBase)
		if len(h.chunks) > 0 {
			last := h.chunks[len(h.chunks)-1]
			base = last.base + uint64(len(last.mem))
			base = (base + chunkSize - 1) / chunkSize * chunkSize
		}
		c = &heapChunk{base: base, mem: make([]byte, sz)}
		h.chunks = append(h.chunks, c)
		off = 0
	}
	c.used = off + n
	addr := c.base + off
	h.allocs[addr] = size
	return addr
}

// free releases an allocation; it reports whether addr was live.
func (h *rawHeap) free(addr uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.allocs[addr]; !ok {
		return false
	}
	delete(h.allocs, addr)
	return true
}

// bytes returns the n bytes at addr, or nil when the range is not mapped.
// Chunks are never reallocated, so the slice stays valid after the lock is
// released; concurrent access to one allocation is the program's data race.
func (h *rawHeap) bytes(addr, n uint64) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.chunks {
		if addr >= c.base && addr-c.base < uint64(len(c.mem)) {
			off := addr - c.base
			if n > uint64(len(c.mem))-off {
				return nil
			}
			return c.mem[off : off+n]
		}
	}
	return nil
}

func (h *rawHeap) handle(v Value) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handles = append(h.handles, v)
	return uint64(len(h.handles))
}

func (h *rawHeap) handleValue(id uint64) Value {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id == 0 || id > uint64(len(h.handles)) {
		return nil
	}
	return h.handles[id-1]
}

// rawAt returns the bytes of a value of type t at addr, trapping with kind
// explicit when the address is not mapped (a raw-pointer obligation).
func (it *Interp) rawAt(addr uint64, t resolve.Type, pos ast.Pos) []byte {
	n := uint64(layout.Of(t).Size)
	b := it.heap.bytes(addr, n)
	if b == nil && n > 0 {
		it.trap("explicit", pos)
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
		return mkInt(x.Name, getInt(b, layout.Of(t).Size))
	case *resolve.RawPtr:
		return mkInt("usize", getInt(b, 8))
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
		s := &Struct{}
		for i, et := range x.Elems {
			s.Fields = append(s.Fields, it.decode(b[li.Fields[i]:], et))
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
			s := &Struct{}
			for i, f := range x.Decl.Fields {
				s.Fields = append(s.Fields, it.decode(b[li.Fields[i]:], resolve.Subst(f.Type, args)))
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
			u := &Union{Tag: tag}
			for i := range vd.Fields {
				u.Fields = append(u.Fields, it.decode(b[offs[i]:], fts[i]))
			}
			return u
		}
	}
	return zeroOf(t)
}

func putInt(b []byte, v uint64, size int) {
	for i := 0; i < size; i++ {
		b[i] = byte(v >> (8 * i))
	}
}

func getInt(b []byte, size int) uint64 {
	var v uint64
	for i := 0; i < size; i++ {
		v |= uint64(b[i]) << (8 * i)
	}
	return v
}
