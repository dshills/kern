package interp

import (
	"syscall"
	"os"
	"io/fs"
	"io"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// intrinsicFn implements a runtime-provided prelude function.
// callCtx carries the call's source position and type arguments.
type callCtx struct {
	pos   ast.Pos
	targs []resolve.Type
}

func (c callCtx) targ(i int) resolve.Type {
	if i < len(c.targs) && c.targs[i] != nil {
		return c.targs[i]
	}
	return &resolve.Unit{}
}

type intrinsicFn func(it *Interp, args []Value, c callCtx) []Value

func acc(v Value) *Access { return v.(*Access) }

// seqOf returns the window of a sequence access; an access to a whole array
// (an array local passed where a sequence view is expected) is opened over
// its elements, in Go slots or raw memory.
func seqOf(a *Access) *seqRef {
	if a.Seq != nil {
		return a.Seq
	}
	if rr, ok := a.Ref.(rawRef); ok {
		return rr.elems()
	}
	arr := a.Ref.get().(*Array)
	return &seqRef{elems: func() []Value { return arr.Elems }, lo: 0, hi: len(arr.Elems)}
}

func u64(v Value) uint64 { return v.(Int).Bits }

// intrinsics is the table of bodyless prelude functions (std/core.kern)
// provided by the interpreter.
var intrinsics = map[string]intrinsicFn{}

func init() {
	for k, v := range map[string]intrinsicFn{
		// output
		"print_u64": func(it *Interp, a []Value, _ callCtx) []Value { _, _ = fmt.Fprint(it.out, a[0].(Int).Bits); return nil },
		"print_i64": func(it *Interp, a []Value, _ callCtx) []Value {
			_, _ = fmt.Fprint(it.out, signedOf(a[0].(Int)))
			return nil
		},
		"print_bool": func(it *Interp, a []Value, _ callCtx) []Value { _, _ = fmt.Fprint(it.out, fmtValue(a[0])); return nil },
		"print_str": func(it *Interp, a []Value, _ callCtx) []Value {
			s := acc(a[0]).Seq
			b := make([]byte, s.length())
			for i := range b {
				b[i] = byte(s.at(i).get().(Int).Bits)
			}
			_, _ = fmt.Fprint(it.out, string(b))
			return nil
		},
		"eprint_str": func(it *Interp, a []Value, _ callCtx) []Value {
			s := acc(a[0]).Seq
			b := make([]byte, s.length())
			for i := range b {
				b[i] = byte(s.at(i).get().(Int).Bits)
			}
			_, _ = it.errOut.Write(b)
			return nil
		},
		"println": func(it *Interp, a []Value, _ callCtx) []Value { _, _ = fmt.Fprintln(it.out); return nil },
		// std/io (hosted): descriptors are indexes into the shared file
		// table (0, 1 and 2 are the standard streams, 1 and 2 through the
		// interpreter's writers); errors are negative errno values
		"io_open": func(it *Interp, a []Value, _ callCtx) []Value {
			path := string(seqBytes(acc(a[0])))
			flags := os.O_RDONLY
			if a[1].(Bool) {
				flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			}
			f, err := os.OpenFile(path, flags, 0o644)
			if err != nil {
				return []Value{mkInt("i32", uint64(uint32(-errnoOf(err))))}
			}
			it.sh.mu.Lock()
			if it.sh.files == nil {
				it.sh.files = map[int64]*os.File{}
			}
			fd := int64(3)
			for it.sh.files[fd] != nil {
				fd++
			}
			it.sh.files[fd] = f
			it.sh.mu.Unlock()
			return []Value{mkInt("i32", uint64(fd))}
		},
		"io_read": func(it *Interp, a []Value, _ callCtx) []Value {
			fd := int64(int32(a[0].(Int).Bits))
			seq := byteSeq(acc(a[1]))
			buf := make([]byte, seq.length())
			var n int
			var err error
			if fd == 0 {
				n, err = os.Stdin.Read(buf)
			} else {
				it.sh.mu.Lock()
				f := it.sh.files[fd]
				it.sh.mu.Unlock()
				if f == nil {
					return []Value{mkInt("i64", ^uint64(8))} // EBADF, -9
				}
				n, err = f.Read(buf)
			}
			if err != nil && n == 0 {
				if errors.Is(err, io.EOF) {
					return []Value{mkInt("i64", 0)}
				}
				return []Value{mkInt("i64", uint64(-errnoOf(err)))}
			}
			for i := 0; i < n; i++ {
				seq.at(i).set(mkInt("u8", uint64(buf[i])))
			}
			return []Value{mkInt("i64", uint64(n))}
		},
		"io_write": func(it *Interp, a []Value, _ callCtx) []Value {
			fd := int64(int32(a[0].(Int).Bits))
			data := seqBytes(acc(a[1]))
			switch fd {
			case 1, 2:
				w := it.out
				if fd == 2 {
					w = it.errOut
				}
				n, err := w.Write(data)
				if err != nil && n == 0 {
					return []Value{mkInt("i64", uint64(-errnoOf(err)))}
				}
				return []Value{mkInt("i64", uint64(n))}
			}
			it.sh.mu.Lock()
			f := it.sh.files[fd]
			it.sh.mu.Unlock()
			if f == nil {
				return []Value{mkInt("i64", ^uint64(8))}
			}
			n, err := f.Write(data)
			if err != nil && n == 0 {
				return []Value{mkInt("i64", uint64(-errnoOf(err)))}
			}
			return []Value{mkInt("i64", uint64(n))}
		},
		"io_close": func(it *Interp, a []Value, _ callCtx) []Value {
			fd := int64(int32(a[0].(Int).Bits))
			it.sh.mu.Lock()
			f := it.sh.files[fd]
			delete(it.sh.files, fd)
			it.sh.mu.Unlock()
			if f == nil {
				return []Value{mkInt("i32", uint64(^uint32(8)))} // EBADF, -9
			}
			if err := f.Close(); err != nil {
				return []Value{mkInt("i32", uint64(uint32(-errnoOf(err))))}
			}
			return []Value{mkInt("i32", 0)}
		},
		"io_remove": func(it *Interp, a []Value, _ callCtx) []Value {
			if err := os.Remove(string(seqBytes(acc(a[0])))); err != nil {
				return []Value{mkInt("i32", uint64(uint32(-errnoOf(err))))}
			}
			return []Value{mkInt("i32", 0)}
		},
		"io_argc": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt("usize", uint64(len(it.sh.args)))}
		},
		"io_arg": func(it *Interp, a []Value, _ callCtx) []Value {
			i := int(u64(a[0]))
			if i < 0 || i >= len(it.sh.args) {
				return []Value{mkInt("usize", 0)}
			}
			arg := []byte(it.sh.args[i])
			seq := byteSeq(acc(a[1]))
			for k := 0; k < len(arg) && k < seq.length(); k++ {
				seq.at(k).set(mkInt("u8", uint64(arg[k])))
			}
			return []Value{mkInt("usize", uint64(len(arg)))}
		},
		"now_ns": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt("u64", uint64(time.Now().UnixNano()))}
		},
		"zero": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{Unit{}} },
		"clone": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{deepClone(it, acc(a[0]).Ref.get(), c.pos)}
		},
		// Shared / Mutex / Atomic
		"Shared.new": func(it *Interp, a []Value, c callCtx) []Value {
			it.runtimeAlloc(c.pos)
			return []Value{&Shared{Val: a[0], Count: 1}}
		},
		"Shared.get": func(it *Interp, a []Value, _ callCtx) []Value {
			s := acc(a[0]).Ref.get().(*Shared)
			return []Value{&Access{Ref: slotRef{&s.Val}}}
		},
		"Shared.clone": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			s := acc(a[0]).Ref.get().(*Shared)
			atomicMu.Lock()
			if s.Count == math.MaxUint64 {
				atomicMu.Unlock()
				it.trap("overflow", pos)
			}
			s.Count++
			atomicMu.Unlock()
			return []Value{s}
		},
		"Mutex.new": func(it *Interp, a []Value, c callCtx) []Value {
			it.runtimeAlloc(c.pos)
			return []Value{&Mutex{Val: a[0], lock: make(chan struct{}, 1)}}
		},
		"Mutex.lock": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			m := acc(a[0]).Ref.get().(*Mutex)
			m.state.Lock()
			recursive := m.Locked && m.owner == it.tid
			m.state.Unlock()
			if recursive {
				it.trap("explicit", pos) // recursive acquisition (spec §13 accepted decision)
			}
			it.acquire(m)
			m.state.Lock()
			m.Locked = true
			m.owner = it.tid
			m.state.Unlock()
			return []Value{&Access{Ref: mutexRef{m}, Edit: true}}
		},
		"Mutex.unlock": func(it *Interp, a []Value, _ callCtx) []Value {
			m := acc(a[0]).Ref.get().(*Mutex)
			m.state.Lock()
			m.Locked = false
			m.state.Unlock()
			<-m.lock
			return nil
		},
		"Condvar.new": func(it *Interp, a []Value, c callCtx) []Value {
			it.runtimeAlloc(c.pos)
			return []Value{&Condvar{gen: make(chan struct{})}}
		},
		"Condvar.wait": func(it *Interp, a []Value, c callCtx) []Value {
			cv := acc(a[0]).Ref.get().(*Condvar)
			// the guard is the edit a Mutex lock bound (the checker requires
			// it): its ref is the Mutex's slot
			mr, ok := acc(a[1]).Ref.(mutexRef)
			if !ok {
				it.trap("explicit", c.pos) // not a lock guard
			}
			m := mr.m
			m.state.Lock()
			held := m.Locked && m.owner == it.tid
			m.state.Unlock()
			if !held {
				it.trap("explicit", c.pos) // the calling thread does not hold the lock
			}
			cv.state.Lock()
			if cv.mutex == nil {
				cv.mutex = m
			}
			same := cv.mutex == m
			gen := cv.gen
			cv.state.Unlock()
			if !same {
				it.trap("explicit", c.pos) // a second Mutex on one Condvar
			}
			m.state.Lock()
			m.Locked = false
			m.state.Unlock()
			<-m.lock
			it.await(gen)
			it.acquire(m)
			m.state.Lock()
			m.Locked = true
			m.owner = it.tid
			m.state.Unlock()
			return nil
		},
		"Condvar.notify_one": func(it *Interp, a []Value, _ callCtx) []Value {
			notifyAll(acc(a[0]).Ref.get().(*Condvar))
			return nil
		},
		"Condvar.notify_all": func(it *Interp, a []Value, _ callCtx) []Value {
			notifyAll(acc(a[0]).Ref.get().(*Condvar))
			return nil
		},
		// an Atomic[T] is a struct of its cell (as natively); atomicMu makes
		// each operation one update of the cell
		"Atomic.new": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{&Struct{Fields: []Value{a[0]}}} },
		"Atomic.load": func(it *Interp, a []Value, _ callCtx) []Value {
			atomicMu.Lock()
			defer atomicMu.Unlock()
			return []Value{atomicLoad(a[0])}
		},
		"Atomic.store": func(it *Interp, a []Value, _ callCtx) []Value {
			atomicUpdate(a[0], func(cell *Value) { *cell = a[1] })
			return nil
		},
		"Atomic.swap": func(it *Interp, a []Value, _ callCtx) []Value {
			var old Value
			atomicUpdate(a[0], func(cell *Value) {
				old = *cell
				*cell = a[1]
			})
			return []Value{old}
		},
		"Atomic.compare_exchange":      atomicCmpxchg,
		"Atomic.compare_exchange_weak": atomicCmpxchg, // never fails spuriously here, which the spec permits
		"Atomic.fetch_add":             atomicRMW("+"),
		"Atomic.fetch_sub":             atomicRMW("-"),
		"Atomic.fetch_and":             atomicRMW("&"),
		"Atomic.fetch_or":              atomicRMW("|"),
		"Atomic.fetch_xor":             atomicRMW("^"),
		"fence":                        func(it *Interp, a []Value, _ callCtx) []Value { return nil },
		// threads: goroutines with their own interpreter copy (PLAN.md 3.3)
		"thread.scope": func(it *Interp, a []Value, c callCtx) []Value {
			it.runtimeAlloc(c.pos)
			return []Value{&Scope{}}
		},
		"thread.spawn": func(it *Interp, a []Value, c callCtx) []Value {
			it.runtimeAlloc(c.pos)
			t := &Thread{done: make(chan struct{})}
			it.spawned(a[0].(FuncVal).Name, a[1:], func(v Value, tr *Trap) { t.Result, t.trap, t.Done = v, tr, true; close(t.done) })
			return []Value{&Union{Tag: 0, Fields: []Value{t}}}
		},
		"scope.spawn": func(it *Interp, a []Value, c callCtx) []Value {
			it.runtimeAlloc(c.pos)
			sc := acc(a[1]).Ref.get().(*Scope)
			j := &Join{done: make(chan struct{})}
			sc.Joins = append(sc.Joins, j)
			it.spawned(a[0].(FuncVal).Name, a[2:], func(v Value, tr *Trap) { j.Result, j.trap, j.Done = v, tr, true; close(j.done) })
			return []Value{&Union{Tag: 0, Fields: []Value{j}}}
		},
		"Thread.join": func(it *Interp, a []Value, _ callCtx) []Value {
			t := a[0].(*Thread)
			it.await(t.done)
			return []Value{t.Result}
		},
		"Join.get": func(it *Interp, a []Value, _ callCtx) []Value {
			j := a[0].(*Join)
			it.await(j.done)
			j.Taken = true
			return []Value{j.Result}
		},
		// trap handler: a seq_cst exchange
		"trap.set_handler": func(it *Interp, a []Value, _ callCtx) []Value {
			f := a[0].(FuncVal)
			it.sh.mu.Lock()
			prev := FuncVal{}
			if it.sh.handler != nil {
				prev = *it.sh.handler
			}
			it.sh.handler = &f
			it.sh.mu.Unlock()
			return []Value{prev}
		},
		// allocator stand-in
		// hosted and freestanding alike: the freestanding runtime serves the
		// system allocator from a static arena (ADR-011)
		"SystemAllocator.new": func(it *Interp, a []Value, c callCtx) []Value { return []Value{&SysAlloc{}} },
		"SystemAllocator.alloc": func(it *Interp, a []Value, c callCtx) []Value {
			size, align := u64(a[1]), u64(a[2])
			// the §6 obligations: a power-of-two alignment up to 4096 and a size that is a multiple of it
			if align == 0 || align&(align-1) != 0 || align > 4096 || size%align != 0 {
				it.trap("explicit", c.pos)
			}
			if it.takeFailNextAlloc() {
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
			}
			addr := it.heap.alloc(size, align)
			if addr == 0 {
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}} // Err(OutOfMemory)
			}
			return []Value{&Union{Tag: 0, Fields: []Value{mkInt("usize", addr)}}}
		},
		"SystemAllocator.alloc_zeroed": func(it *Interp, a []Value, c callCtx) []Value {
			size, align := u64(a[1]), u64(a[2])
			if align == 0 || align&(align-1) != 0 || align > 4096 || size%align != 0 {
				it.trap("explicit", c.pos)
			}
			if it.takeFailNextAlloc() {
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
			}
			addr := it.heap.alloc(size, align)
			if addr == 0 {
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}} // Err(OutOfMemory)
			}
			it.heap.zero(addr, size)
			return []Value{&Union{Tag: 0, Fields: []Value{mkInt("usize", addr)}}}
		},
		"SystemAllocator.realloc": func(it *Interp, a []Value, c callCtx) []Value {
			addr, oldSize, size, align := u64(a[1]), u64(a[2]), u64(a[3]), u64(a[4])
			if align == 0 || align&(align-1) != 0 || align > 4096 || size%align != 0 {
				it.trap("explicit", c.pos)
			}
			if it.takeFailNextAlloc() {
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
			}
			n, live := it.heap.realloc(addr, oldSize, size, align)
			if !live {
				it.trap("explicit", c.pos) // not a live allocation of this allocator
			}
			if n == 0 {
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
			}
			return []Value{&Union{Tag: 0, Fields: []Value{mkInt("usize", n)}}}
		},
		"SystemAllocator.free": func(it *Interp, a []Value, c callCtx) []Value {
			if !it.heap.free(u64(a[1])) {
				it.trap("explicit", c.pos) // not a live allocation of this allocator
			}
			return nil
		},
		// raw pointers (spec §6) over the byte-addressed heap
		"ptr_null":      func(it *Interp, a []Value, _ callCtx) []Value { return []Value{mkInt("usize", 0)} },
		"ptr_from_addr": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{mkInt("usize", u64(a[0]))} },
		"ptr_addr":      func(it *Interp, a []Value, _ callCtx) []Value { return []Value{mkInt("usize", u64(a[0]))} },
		"ptr_cast":      func(it *Interp, a []Value, _ callCtx) []Value { return []Value{mkInt("usize", u64(a[0]))} },
		"ptr_offset": func(it *Interp, a []Value, c callCtx) []Value {
			n := signedOf(a[1].(Int))
			size := int64(layout.Of(c.targ(0)).Size)
			return []Value{mkInt("usize", uint64(int64(u64(a[0]))+n*size))}
		},
		"ptr_load": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{it.decode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0))}
		},
		"ptr_take": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{it.decode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0))}
		},
		"ptr_store": func(it *Interp, a []Value, c callCtx) []Value {
			it.encode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0), a[1])
			return nil
		},
		"ptr_load_volatile": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{it.decode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0))}
		},
		"ptr_store_volatile": func(it *Interp, a []Value, c callCtx) []Value {
			if it.consoleWrite(u64(a[0]), a[1]) {
				return nil
			}
			it.encode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0), a[1])
			return nil
		},
		"ptr_copy": func(it *Interp, a []Value, c callCtx) []Value {
			n := u64(a[2]) * uint64(layout.Of(c.targ(0)).Size)
			dst := it.heap.bytes(u64(a[0]), n)
			src := it.heap.bytes(u64(a[1]), n)
			if (dst == nil || src == nil) && n > 0 {
				it.trap("explicit", c.pos)
			}
			copy(dst, src)
			return nil
		},
		// ptr_store_seq writes a sequence view's elements into raw memory
		// element by element: the source may be Go slots (a view of an array
		// local or of a Vec) or raw memory, and only the destination is raw
		"ptr_store_seq": func(it *Interp, a []Value, c callCtx) []Value {
			s := seqOf(acc(a[1]))
			t := c.targ(0)
			size := uint64(layout.Of(t).Size)
			base := u64(a[0])
			for i := 0; i < s.length(); i++ {
				it.encode(it.rawAt(base+uint64(i)*size, t, c.pos), t, s.at(i).get())
			}
			return nil
		},
		// accesses on raw memory (the kern containers' elements)
		"ptr_view": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{&Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}}}
		},
		"ptr_edit": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{&Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}, Edit: true}}
		},
		// ptr_init: the initializer's out parameter is an access on the raw
		// storage, as natively it is the pointer itself
		"ptr_init": func(it *Interp, a []Value, c callCtx) []Value {
			out := &Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}, Edit: true}
			return it.callNamed(a[1].(FuncVal).Name, []Value{out})
		},
		// ptr_init_ctx: as ptr_init, with the context access passed on as
		// the initializer's second argument
		"ptr_init_ctx": func(it *Interp, a []Value, c callCtx) []Value {
			out := &Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}, Edit: true}
			return it.callNamed(a[1].(FuncVal).Name, []Value{out, a[2]})
		},
		"saturating_add": saturating("+"),
		"saturating_sub": saturating("-"),
		"saturating_mul": saturating("*"),
		"checked_add":    checked("+"),
		"checked_sub":    checked("-"),
		"checked_mul":    checked("*"),
		"checked_div":    checked("/"),
		"checked_rem":    checked("%"),
		"checked_shl":    checked("<<"),
		"checked_shr":    checked(">>"),
		"load_be":        func(it *Interp, a []Value, c callCtx) []Value { return []Value{it.loadWide(a, c, true)} },
		"load_le":        func(it *Interp, a []Value, c callCtx) []Value { return []Value{it.loadWide(a, c, false)} },
		"store_be":       func(it *Interp, a []Value, c callCtx) []Value { it.storeWide(a, c, true); return nil },
		"store_le":       func(it *Interp, a []Value, c callCtx) []Value { it.storeWide(a, c, false); return nil },
		"ptr_view_seq": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{&Access{Seq: &seqRef{raw: &rawSeq{it: it, base: u64(a[0]), elem: c.targ(0)}, lo: 0, hi: int(u64(a[1]))}}}
		},
		"ptr_edit_seq": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{&Access{Seq: &seqRef{raw: &rawSeq{it: it, base: u64(a[0]), elem: c.targ(0)}, lo: 0, hi: int(u64(a[1]))}, Edit: true}}
		},
		"ptr_edit2": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{&Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}, Edit: true}, &Access{Ref: rawRef{it: it, addr: u64(a[1]), t: c.targ(0)}, Edit: true}}
		},
		"size_of": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{mkInt("usize", uint64(layout.Of(c.targ(0)).Size))}
		},
		"align_of": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{mkInt("usize", uint64(layout.Of(c.targ(0)).Align))}
		},
		"container_identity": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt("u32", uint64(it.newIdentity()))}
		},
		"asm": func(it *Interp, a []Value, c callCtx) []Value { it.trap("explicit", c.pos); return nil },
		// SIMD: lane-wise over arrays, wrapping
		"simd_add": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{it.lanes("+", a[0], a[1])} },
		"simd_sub": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{it.lanes("-", a[0], a[1])} },
		"simd_mul": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{it.lanes("*", a[0], a[1])} },
		"simd_and": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{it.lanes("&", a[0], a[1])} },
		"simd_or":  func(it *Interp, a []Value, _ callCtx) []Value { return []Value{it.lanes("|", a[0], a[1])} },
		"simd_xor": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{it.lanes("^", a[0], a[1])} },
		"simd_shuffle": func(it *Interp, a []Value, c callCtx) []Value {
			x, y, idx := a[0].(*Array), a[1].(*Array), a[2].(*Array)
			out := &Array{Elems: make([]Value, len(x.Elems))}
			n := uint64(len(x.Elems))
			for i, iv := range idx.Elems {
				k := u64(iv)
				switch {
				case k < n:
					out.Elems[i] = x.Elems[k]
				case k < 2*n:
					out.Elems[i] = y.Elems[k-n]
				default:
					it.trap("bounds", c.pos)
				}
			}
			return []Value{out}
		},
		"simd_reduce_add": func(it *Interp, a []Value, _ callCtx) []Value {
			x := a[0].(*Array)
			acc := x.Elems[0]
			for _, e := range x.Elems[1:] {
				acc = it.binary("+", acc, e, "wrapping")
			}
			return []Value{acc}
		},
		"exit":      func(it *Interp, a []Value, _ callCtx) []Value { panic(exitCode(signedOf(a[0].(Int)))) },
		"yield_now": func(it *Interp, a []Value, _ callCtx) []Value { runtime.Gosched(); return nil },
		// test hooks
		"testing.require_hooks": func(it *Interp, a []Value, c callCtx) []Value {
			if !it.TestHooks {
				it.trap("explicit", c.pos)
			}
			return nil
		},
		"testing.fail_next_alloc": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			if !it.TestHooks {
				it.trap("explicit", pos)
			}
			it.sh.mu.Lock()
			it.sh.failNextAlloc = true
			it.sh.mu.Unlock()
			return nil
		},
		"testing.seed_shared_count": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			if !it.TestHooks {
				it.trap("explicit", pos)
			}
			acc(a[0]).Ref.get().(*Shared).Count = u64(a[1])
			return nil
		},
	} {
		intrinsics[k] = v
	}
}

// atomicLoad reads the cell behind a view of an Atomic (its struct's only
// field) under atomicMu.
func atomicLoad(v Value) Value { return acc(v).Ref.get().(*Struct).Fields[0] }

// atomicUpdate applies f to the cell behind a view of an Atomic and writes
// the struct back through the reference, all under atomicMu: a cell in raw
// memory (a container element, a boxed value) is decoded afresh by every
// get, so an update reaches it only through set.
func atomicUpdate(v Value, f func(cell *Value)) {
	atomicMu.Lock()
	defer atomicMu.Unlock()
	ref := acc(v).Ref
	s := ref.get().(*Struct)
	f(&s.Fields[0])
	ref.set(s)
}

func atomicCmpxchg(it *Interp, a []Value, _ callCtx) []Value {
	var r Value
	atomicUpdate(a[0], func(cell *Value) {
		if equalValues(*cell, a[1]) {
			old := *cell
			*cell = a[2]
			r = &Union{Tag: 0, Fields: []Value{old}}
			return
		}
		r = &Union{Tag: 1, Fields: []Value{*cell}}
	})
	return []Value{r}
}

func atomicRMW(op string) intrinsicFn {
	return func(it *Interp, a []Value, _ callCtx) []Value {
		var old Value
		atomicUpdate(a[0], func(cell *Value) {
			old = *cell
			*cell = it.binary(op, old, a[1], "wrapping")
		})
		return []Value{old}
	}
}

// deepClone duplicates a value including the runtime-provided library
// objects (the `clone` intrinsic behind copy(x) for move-only types; the
// kern containers clone themselves through their clone methods).
func deepClone(it *Interp, v Value, pos ast.Pos) Value {
	switch x := v.(type) {
	case *Shared:
		// a clone of a Shared is another owner of the same value
		if x.Count == math.MaxUint64 {
			it.trap("overflow", pos)
		}
		x.Count++
		return x
	case *Struct, *Union, *Array:
		// aggregates: clone fields recursively (library objects inside them too)
		switch y := x.(type) {
		case *Struct:
			n := &Struct{Fields: make([]Value, len(y.Fields))}
			for i, f := range y.Fields {
				n.Fields[i] = deepClone(it, f, pos)
			}
			return n
		case *Union:
			n := &Union{Tag: y.Tag, Fields: make([]Value, len(y.Fields))}
			for i, f := range y.Fields {
				n.Fields[i] = deepClone(it, f, pos)
			}
			return n
		case *Array:
			n := &Array{Elems: make([]Value, len(y.Elems))}
			for i, e := range y.Elems {
				n.Elems[i] = deepClone(it, e, pos)
			}
			return n
		}
	}
	return v
}

// lanes applies a wrapping binary operation lane by lane.
func (it *Interp) lanes(op string, l, r Value) Value {
	x, y := l.(*Array), r.(*Array)
	out := &Array{Elems: make([]Value, len(x.Elems))}
	for i := range x.Elems {
		out.Elems[i] = it.binary(op, x.Elems[i], y.Elems[i], "wrapping")
	}
	return out
}

// consoleWrite emulates the freestanding console device (PLAN.md 3.1): in
// freestanding mode a volatile byte store to the console address writes the
// byte to standard output. It reports whether the store was the device's.
func (it *Interp) consoleWrite(addr uint64, v Value) bool {
	if !it.Freestanding || addr != ConsoleAddr {
		return false
	}
	if b, ok := v.(Int); ok {
		_, _ = it.out.Write([]byte{byte(b.Bits)})
	}
	return true
}

// ConsoleAddr is the freestanding console device: the PL011 UART of the
// QEMU virt machine (AArch64), emulated by the interpreter.
const ConsoleAddr = 0x0900_0000

// acquire takes m's lock. A thread waiting for it ends, as at a block
// boundary, when the program terminates meanwhile (Stop, or a trap or exit
// on another thread), so a stopped run never stays blocked in a lock
// (specs/test-runs/SPEC.md R4).
func (it *Interp) acquire(m *Mutex) {
	select {
	case m.lock <- struct{}{}:
		return
	default:
	}
	select {
	case m.lock <- struct{}{}:
	case <-it.sh.fatalCh:
		if code, ok := it.terminated(); ok {
			panic(exitCode(code))
		}
	}
}

// atomicMu serializes the interpreter's atomic and reference-count updates.
var atomicMu sync.Mutex

// notifyAll wakes every thread waiting on cv: the current generation's
// channel is closed and a fresh one opened for later waiters.
func notifyAll(cv *Condvar) {
	cv.state.Lock()
	close(cv.gen)
	cv.gen = make(chan struct{})
	cv.state.Unlock()
}

// spawned starts a thread: a goroutine running the function on a copy of
// the interpreter with fresh thread-locals. A trap on the thread writes its
// record there and makes the program terminate at the next join or at its
// end (natively the process aborts at once).
func (it *Interp) spawned(name string, args []Value, finish func(Value, *Trap)) {
	child := it.child()
	go func() {
		var result Value = Unit{}
		var trapped *Trap
		func() {
			defer func() {
				if r := recover(); r != nil {
					tr, ok := r.(*Trap)
					if !ok {
						if code, isExit := r.(exitCode); isExit {
							it.fatalExit(code) // exit on any thread ends the program with that status
							return
						}
						panic(r)
					}
					child.trapRecord(tr)
					trapped = tr
				}
			}()
			rets := child.callNamed(name, args)
			if len(rets) > 0 {
				result = rets[0]
			}
		}()
		if trapped != nil {
			it.fatalTrap(trapped)
		}
		finish(result, trapped)
	}()
}

// loadWide reads the bytes of an integer of the call's type argument from a
// sequence access at an offset, in big- or little-endian order, and decodes
// them as the interpreter's memory representation (little-endian); the
// bounds check on the last byte precedes the call.
func (it *Interp) loadWide(a []Value, c callCtx, be bool) Value {
	t := c.targ(0)
	seq := byteSeq(a[0].(*Access))
	at := int(u64(a[1]))
	n := layout.Of(t).Size
	buf := make([]byte, n)
	for i := range n {
		b := byte(u64(seq.at(at + i).get()))
		if be {
			buf[n-1-i] = b
		} else {
			buf[i] = b
		}
	}
	return it.decode(buf, t)
}

// storeWide writes the bytes of an integer of the call's type argument
// (a[2]) into a sequence access at an offset, in big- or little-endian
// order; the bounds check on the last byte precedes the call.
func (it *Interp) storeWide(a []Value, c callCtx, be bool) {
	t := c.targ(0)
	seq := byteSeq(a[0].(*Access))
	at := int(u64(a[1]))
	n := layout.Of(t).Size
	buf := make([]byte, n)
	it.encode(buf, t, a[2])
	for i := range n {
		b := buf[i]
		if be {
			b = buf[n-1-i]
		}
		seq.at(at + i).set(mkInt("u8", uint64(b)))
	}
}

// seqBytes copies the bytes a sequence access denotes.
func seqBytes(a *Access) []byte {
	seq := byteSeq(a)
	b := make([]byte, seq.length())
	for i := range b {
		b[i] = byte(u64(seq.at(i).get()))
	}
	return b
}

// errnoOf maps an I/O error to the errno std/io reports.
func errnoOf(err error) int {
	var en syscall.Errno
	if errors.As(err, &en) {
		return int(en)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return 2 // ENOENT
	case errors.Is(err, fs.ErrPermission):
		return 13 // EACCES
	case errors.Is(err, fs.ErrExist):
		return 17 // EEXIST
	}
	return 5 // EIO
}

// byteSeq is the sequence window of an access to bytes: the access's own
// window, or, for an access opened on a whole array (in raw memory or a
// local), the array's elements.
func byteSeq(acc *Access) *seqRef {
	if acc.Seq != nil {
		return acc.Seq
	}
	if rr, ok := acc.Ref.(rawRef); ok {
		return rr.elems()
	}
	arr := acc.Ref.get().(*Array)
	return &seqRef{elems: func() []Value { return arr.Elems }, lo: 0, hi: len(arr.Elems)}
}

// checked is a §4.1 checked form: None where the trapping form would trap,
// Some(result) otherwise (Option's variants are Some then None).
func checked(op string) func(*Interp, []Value, callCtx) []Value {
	return func(it *Interp, a []Value, _ callCtx) []Value {
		x, y := a[0].(Int), a[1].(Int)
		if arithTrap(op, x, y) != "" {
			return []Value{&Union{Tag: 1}}
		}
		return []Value{&Union{Tag: 0, Fields: []Value{it.binary(op, x, y, "wrapping")}}}
	}
}

// saturating is a §4.1 saturating form: the result when it fits, else the
// type's limit in the direction of the overflow.
func saturating(op string) func(*Interp, []Value, callCtx) []Value {
	return func(it *Interp, a []Value, _ callCtx) []Value {
		x, y := a[0].(Int), a[1].(Int)
		if arithTrap(op, x, y) == "" {
			return []Value{it.binary(op, x, y, "wrapping")}
		}
		ty := x.Ty
		w := width(ty)
		if !signed(ty) {
			if op == "-" {
				return []Value{mkInt(ty, 0)}
			}
			return []Value{mkInt(ty, ^uint64(0))}
		}
		sx, sy := signedOf(x), signedOf(y)
		toMax := false
		switch op {
		case "+":
			toMax = sy > 0
		case "-":
			toMax = sy < 0
		case "*":
			toMax = (sx < 0) == (sy < 0)
		}
		if toMax {
			return []Value{mkInt(ty, uint64(maxSigned(w)))}
		}
		return []Value{mkInt(ty, uint64(minSigned(w)))}
	}
}
