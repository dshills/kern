package interp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// startEnv is the environment the process started with (std/process),
// taken during package initialisation, before any code can change it. Go
// keeps only the first entry of a name that occurs more than once, and
// the native runtime does the same, so both engines see one list.
var startEnv = syscall.Environ()

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
		// seqBytes: the argument may be a slice or a whole array
		"print_str": func(it *Interp, a []Value, _ callCtx) []Value {
			_, _ = it.out.Write(seqBytes(acc(a[0])))
			return nil
		},
		"eprint_str": func(it *Interp, a []Value, _ callCtx) []Value {
			_, _ = it.errOut.Write(seqBytes(acc(a[0])))
			return nil
		},
		"println": func(it *Interp, a []Value, _ callCtx) []Value { _, _ = fmt.Fprintln(it.out); return nil },
		// the interpreter does not buffer standard output, so a flush has
		// nothing to write; it reports and clears the first failed write,
		// as the runtime does (specs/runtime RB3)
		"io_flush_stdout": func(it *Interp, a []Value, _ callCtx) []Value {
			if l, ok := it.out.(*lockedWriter); ok {
				if err := l.takeErr(); err != nil {
					return []Value{mkInt(tyI32, uint64(uint32(-errnoOf(err))))}
				}
			}
			return []Value{mkInt(tyI32, 0)}
		},
		"io_is_terminal": func(it *Interp, a []Value, _ callCtx) []Value {
			tty := false
			switch int32(a[0].(Int).Bits) {
			case 0:
				tty = isTerminal(os.Stdin)
			case 1:
				tty = isTerminal(it.out)
			case 2:
				tty = isTerminal(it.errOut)
			}
			if tty {
				return []Value{mkInt(tyI32, 1)}
			}
			return []Value{mkInt(tyI32, 0)}
		},
		// std/io (hosted): descriptors are indexes into the shared file
		// table (0, 1 and 2 are the standard streams, 1 and 2 through the
		// interpreter's writers); errors are negative errno values
		"io_open": func(it *Interp, a []Value, _ callCtx) []Value {
			path, e := cPath(seqBytes(acc(a[0])))
			if e != 0 {
				return []Value{mkInt(tyI32, uint64(uint32(-e)))}
			}
			f, err := os.OpenFile(path, openFlags(uint32(a[1].(Int).Bits)), 0o666)
			if err != nil {
				return []Value{mkInt(tyI32, uint64(uint32(-errnoOf(err))))}
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
			if it.sh.fileOpts == nil {
				it.sh.fileOpts = map[int64]uint32{}
			}
			it.sh.fileOpts[fd] = uint32(a[1].(Int).Bits)
			it.sh.mu.Unlock()
			return []Value{mkInt(tyI32, uint64(fd))}
		},
		"io_read": func(it *Interp, a []Value, _ callCtx) []Value {
			fd := int64(int32(a[0].(Int).Bits))
			seq := byteSeq(acc(a[1]))
			buf := make([]byte, seq.length())
			var n int
			var err error
			if fd == 0 {
				if p := it.pumpIfStarted(); p != nil {
					r := p.read(buf, 0, false)
					if r > 0 {
						for i := 0; i < int(r); i++ {
							seq.at(i).set(mkInt(tyU8, uint64(buf[i])))
						}
					}
					return []Value{mkInt(tyI64, uint64(r))}
				}
				n, err = os.Stdin.Read(buf)
			} else {
				it.sh.mu.Lock()
				f := it.sh.files[fd]
				it.sh.mu.Unlock()
				if f == nil {
					return []Value{mkInt(tyI64, ^uint64(8))} // EBADF, -9
				}
				n, err = f.Read(buf)
			}
			if err != nil && n == 0 {
				if errors.Is(err, io.EOF) {
					return []Value{mkInt(tyI64, 0)}
				}
				return []Value{mkInt(tyI64, uint64(-errnoOf(err)))}
			}
			for i := 0; i < n; i++ {
				seq.at(i).set(mkInt(tyU8, uint64(buf[i])))
			}
			return []Value{mkInt(tyI64, uint64(n))}
		},
		// io_read_timeout (specs/std-timeouts): one read, waiting at most
		// the given nanoseconds for the descriptor to be ready
		"io_read_timeout": func(it *Interp, a []Value, _ callCtx) []Value {
			fd := int64(int32(a[0].(Int).Bits))
			seq := byteSeq(acc(a[1]))
			if seq.length() == 0 {
				return []Value{mkInt(tyI64, 0)}
			}
			timeout := time.Duration(min(a[2].(Int).Bits, uint64(1<<63-1)))
			buf := make([]byte, seq.length())
			var r int64
			if fd == 0 {
				r = it.stdinPump().read(buf, timeout, true)
			} else {
				f := it.openFile(fd)
				if f == nil {
					return []Value{mkInt(tyI64, ^uint64(8))} // EBADF, -9
				}
				n, err := readTimeout(f, buf, timeout)
				switch {
				case n > 0 || err == nil:
					r = int64(n)
				case errors.Is(err, io.EOF):
					r = 0
				case errors.Is(err, os.ErrDeadlineExceeded):
					r = timedOut
				default:
					r = -int64(errnoOf(err))
				}
			}
			for i := 0; i < int(r); i++ {
				seq.at(i).set(mkInt(tyU8, uint64(buf[i])))
			}
			return []Value{mkInt(tyI64, uint64(r))}
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
					return []Value{mkInt(tyI64, uint64(-errnoOf(err)))}
				}
				return []Value{mkInt(tyI64, uint64(n))}
			}
			it.sh.mu.Lock()
			f := it.sh.files[fd]
			it.sh.mu.Unlock()
			if f == nil {
				return []Value{mkInt(tyI64, ^uint64(8))}
			}
			n, err := f.Write(data)
			if err != nil && n == 0 {
				return []Value{mkInt(tyI64, uint64(-errnoOf(err)))}
			}
			return []Value{mkInt(tyI64, uint64(n))}
		},
		"io_close": func(it *Interp, a []Value, _ callCtx) []Value {
			fd := int64(int32(a[0].(Int).Bits))
			it.sh.mu.Lock()
			f := it.sh.files[fd]
			delete(it.sh.files, fd)
			it.sh.mu.Unlock()
			if f == nil {
				return []Value{mkInt(tyI32, uint64(^uint32(8)))} // EBADF, -9
			}
			if err := f.Close(); err != nil {
				return []Value{mkInt(tyI32, uint64(uint32(-errnoOf(err))))}
			}
			return []Value{mkInt(tyI32, 0)}
		},
		"io_remove": func(it *Interp, a []Value, _ callCtx) []Value {
			// unlink, as natively: os.Remove would also remove an empty
			// directory
			return pathOp(a[0], syscall.Unlink)
		},
		// directories and temporary files (specs/std-files), as the hosted
		// runtime does them
		"io_mkdir": func(it *Interp, a []Value, _ callCtx) []Value {
			return pathOp(a[0], func(p string) error { return syscall.Mkdir(p, 0o777) })
		},
		"io_rmdir": func(it *Interp, a []Value, _ callCtx) []Value { return pathOp(a[0], syscall.Rmdir) },
		"io_kind": func(it *Interp, a []Value, _ callCtx) []Value {
			p, e := cPath(seqBytes(acc(a[0])))
			if e != 0 {
				return []Value{mkInt(tyI32, uint64(uint32(-e)))}
			}
			fi, err := os.Stat(p)
			if err != nil {
				return []Value{mkInt(tyI32, uint64(uint32(-errnoOf(err))))}
			}
			k := 3
			switch {
			case fi.Mode().IsRegular():
				k = 1
			case fi.IsDir():
				k = 2
			}
			return []Value{mkInt(tyI32, uint64(k))}
		},
		"io_list": func(it *Interp, a []Value, _ callCtx) []Value {
			p, e := cPath(seqBytes(acc(a[0])))
			if e != 0 {
				return []Value{mkInt(tyI64, uint64(-int64(e)))}
			}
			// Names only, as the runtime reads them. os.ReadDir also
			// asks what each entry is, and fails the whole listing when
			// the system will not say for one of them: /dev/fd on macOS
			// 26 gives no type, and a descriptor that cannot be examined
			// (the host's own kqueue) then made every listing fail.
			d, err := os.Open(p)
			if err != nil {
				return []Value{mkInt(tyI64, uint64(-int64(errnoOf(err))))}
			}
			names, err := d.Readdirnames(-1)
			_ = d.Close()
			if err != nil {
				return []Value{mkInt(tyI64, uint64(-int64(errnoOf(err))))}
			}
			sort.Strings(names) // bytewise, as strcmp sorts natively
			var out []byte
			for _, name := range names {
				out = append(append(out, name...), 0)
			}
			buf := byteSeq(acc(a[1]))
			if len(out) <= buf.length() {
				for i, b := range out {
					buf.at(i).set(mkInt(tyU8, uint64(b)))
				}
			}
			return []Value{mkInt(tyI64, uint64(len(out)))}
		},
		"io_temp": func(it *Interp, a []Value, _ callCtx) []Value {
			fail := func(e int) []Value { return []Value{mkInt(tyI64, uint64(-int64(e)))} }
			prefix := seqBytes(acc(a[0]))
			if len(prefix) > 255 {
				return fail(int(syscall.ENAMETOOLONG))
			}
			if bytes.IndexByte(prefix, 0) >= 0 || bytes.IndexByte(prefix, '/') >= 0 {
				return fail(int(syscall.EINVAL))
			}
			var name string
			if a[1].(Bool) {
				d, err := os.MkdirTemp("", string(prefix)+"*")
				if err != nil {
					return fail(errnoOf(err))
				}
				name = d
			} else {
				f, err := os.CreateTemp("", string(prefix)+"*")
				if err != nil {
					return fail(errnoOf(err))
				}
				name = f.Name()
				if err := f.Close(); err != nil {
					_ = os.Remove(name)
					return fail(errnoOf(err))
				}
			}
			buf := byteSeq(acc(a[2]))
			if len(name) > buf.length() {
				_ = os.Remove(name)
				return fail(int(syscall.ENAMETOOLONG))
			}
			for i := 0; i < len(name); i++ {
				buf.at(i).set(mkInt(tyU8, uint64(name[i])))
			}
			return []Value{mkInt(tyI64, uint64(len(name)))}
		},
		// std/process (hosted): the start-time environment
		"proc_env_count": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyUsize, uint64(len(startEnv)))}
		},
		"proc_env_at": func(it *Interp, a []Value, _ callCtx) []Value {
			i := u64(a[0])
			if i >= uint64(len(startEnv)) {
				return []Value{mkInt(tyUsize, 0)}
			}
			e := startEnv[i]
			seq := byteSeq(acc(a[1]))
			for k := 0; k < len(e) && k < seq.length(); k++ {
				seq.at(k).set(mkInt(tyU8, uint64(e[k])))
			}
			return []Value{mkInt(tyUsize, uint64(len(e)))}
		},
		"io_argc": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyUsize, uint64(len(it.sh.args)))}
		},
		"io_arg": func(it *Interp, a []Value, _ callCtx) []Value {
			i := int(u64(a[0]))
			if i < 0 || i >= len(it.sh.args) {
				return []Value{mkInt(tyUsize, 0)}
			}
			arg := []byte(it.sh.args[i])
			seq := byteSeq(acc(a[1]))
			for k := 0; k < len(arg) && k < seq.length(); k++ {
				seq.at(k).set(mkInt(tyU8, uint64(arg[k])))
			}
			return []Value{mkInt(tyUsize, uint64(len(arg)))}
		},
		// now_ns is monotonic: the wall clock at the first reading plus the
		// monotonic time since (specs/std-time R5)
		"now_ns": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyU64, monotonicNow())}
		},
		"time_sleep_ns": func(it *Interp, a []Value, _ callCtx) []Value {
			for _, d := range sleepChunks(u64(a[0])) {
				time.Sleep(d)
			}
			return nil
		},
		// std/str floating-point text (specs/float-text)
		"str_f64_digits": func(it *Interp, a []Value, _ callCtx) []Value {
			text := f64Digits(a[0].(Float).V)
			seq := byteSeq(acc(a[1]))
			n := 0
			for ; n < len(text) && n < seq.length(); n++ {
				seq.at(n).set(mkInt(tyU8, uint64(text[n])))
			}
			return []Value{mkInt(tyUsize, uint64(n))}
		},
		"str_parse_f64": func(it *Interp, a []Value, _ callCtx) []Value {
			v := parseF64(seqBytes(acc(a[0])))
			return []Value{mkFloat("f64", v)}
		},
		// std/mem: the interpreter's pages are blocks of its own heap,
		// aligned to the host's page size
		"mem_page_size": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyUsize, uint64(os.Getpagesize()))}
		},
		"mem_map": func(it *Interp, a []Value, _ callCtx) []Value {
			page, bytes := uint64(os.Getpagesize()), u64(a[0])
			if bytes == 0 || bytes%page != 0 {
				return []Value{mkInt(tyUsize, 0)}
			}
			addr := it.heap.alloc(bytes, page)
			if addr != 0 {
				it.heap.zero(addr, bytes)
			}
			return []Value{mkInt(tyUsize, addr)}
		},
		"mem_unmap": func(it *Interp, a []Value, c callCtx) []Value {
			it.unmapFile(u64(a[0])) // a file mapping's changes go to its file first
			if !it.heap.is(u64(a[0]), u64(a[1]), uint64(os.Getpagesize())) || !it.heap.free(u64(a[0])) {
				it.trap("explicit", c.pos) // not a live mapping of this size
			}
			return nil
		},
		"time_wall_ns": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyU64, uint64(time.Now().UnixNano()))}
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
		// the try_new forms (specs/fallible-runtime-objects): Err(OutOfMemory)
		// when the allocation fails, after destroying the moved value; the
		// interpreter's allocation fails only through the test hook
		"Shared.try_new": func(it *Interp, a []Value, c callCtx) []Value {
			if it.takeFailNextAlloc() {
				it.destroy(a[0], c.targ(0))
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
			}
			return []Value{&Union{Tag: 0, Fields: []Value{&Shared{Val: a[0], Count: 1}}}}
		},
		"Mutex.try_new": func(it *Interp, a []Value, c callCtx) []Value {
			if it.takeFailNextAlloc() {
				it.destroy(a[0], c.targ(0))
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
			}
			return []Value{&Union{Tag: 0, Fields: []Value{&Mutex{Val: a[0], lock: make(chan struct{}, 1)}}}}
		},
		"Condvar.try_new": func(it *Interp, a []Value, c callCtx) []Value {
			if it.takeFailNextAlloc() {
				return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
			}
			return []Value{&Union{Tag: 0, Fields: []Value{&Condvar{gen: make(chan struct{})}}}}
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
		// try_acquire takes the lock only if it is free; held is the edit
		// of the guarded value for a caller that holds it (Mutex.try_with,
		// specs/runtime RD3)
		"Mutex.try_acquire": func(it *Interp, a []Value, _ callCtx) []Value {
			m := acc(a[0]).Ref.get().(*Mutex)
			select {
			case m.lock <- struct{}{}:
			default:
				return []Value{Bool(false)} // held, by any thread: no trap
			}
			m.state.Lock()
			m.Locked = true
			m.owner = it.tid
			m.state.Unlock()
			return []Value{Bool(true)}
		},
		"Mutex.held": func(it *Interp, a []Value, _ callCtx) []Value {
			m := acc(a[0]).Ref.get().(*Mutex)
			return []Value{&Access{Ref: mutexRef{m}, Edit: true}}
		},
		// parking (specs/runtime RD1)
		"park": func(it *Interp, a []Value, _ callCtx) []Value {
			if gen, wait := it.parkOn(a[0], a[1]); wait {
				it.await(gen)
			}
			return nil
		},
		"park_ns": func(it *Interp, a []Value, _ callCtx) []Value {
			gen, wait := it.parkOn(a[0], a[1])
			if !wait {
				return []Value{Bool(true)} // the value already differs
			}
			return []Value{Bool(it.awaitFor(gen, u64(a[2])))}
		},
		"unpark_one": func(it *Interp, a []Value, _ callCtx) []Value { it.unpark(); return nil },
		"unpark_all": func(it *Interp, a []Value, _ callCtx) []Value { it.unpark(); return nil },
		"Condvar.wait_ns": func(it *Interp, a []Value, c callCtx) []Value {
			m, gen := it.condvarRelease(a, c)
			woken := it.awaitFor(gen, u64(a[2]))
			it.condvarReacquire(m)
			return []Value{Bool(woken)}
		},
		"Condvar.new": func(it *Interp, a []Value, c callCtx) []Value {
			it.runtimeAlloc(c.pos)
			return []Value{&Condvar{gen: make(chan struct{})}}
		},
		"Condvar.wait": func(it *Interp, a []Value, c callCtx) []Value {
			m, gen := it.condvarRelease(a, c)
			it.await(gen)
			it.condvarReacquire(m)
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
			// a failed spawn, by the spawn hook or by the allocation of
			// what the thread needs, is Err(SpawnErr) and leaves nothing
			// behind (specs/runtime RC3)
			if it.takeFailNextSpawn() || it.takeFailNextAlloc() {
				return spawnErrValue() // the kern side destroys the own arguments
			}
			t := &Thread{done: make(chan struct{})}
			it.spawned(a[0].(FuncVal).Name, a[1:], func(child *Interp, v Value, tr *Trap) {
				t.mu.Lock()
				t.Result, t.trap, t.Done = v, tr, true
				detached, elem := t.detached, t.elem
				t.mu.Unlock()
				close(t.done)
				if detached && tr == nil {
					child.destroy(v, elem) // nobody will take the result
				}
			})
			return []Value{&Union{Tag: 0, Fields: []Value{t}}}
		},
		"thread.set_stack_bytes": func(it *Interp, a []Value, _ callCtx) []Value {
			n := u64(a[0])
			if n < stackBytesMin || n > stackBytesMax {
				return []Value{Bool(false)}
			}
			it.sh.stackAPI.Store(n)
			return []Value{Bool(true)}
		},
		"thread.stack_bytes": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyUsize, it.nextStackBytes())}
		},
		"thread.available_parallelism": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyUsize, uint64(runtime.NumCPU()))}
		},
		"thread.current_id": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyU64, it.tid)}
		},
		"scope.spawn": func(it *Interp, a []Value, c callCtx) []Value {
			if it.takeFailNextSpawn() || it.takeFailNextAlloc() {
				return spawnErrValue()
			}
			sc := acc(a[1]).Ref.get().(*Scope)
			j := &Join{done: make(chan struct{})}
			sc.Joins = append(sc.Joins, j)
			it.spawned(a[0].(FuncVal).Name, a[2:], func(_ *Interp, v Value, tr *Trap) { j.Result, j.trap, j.Done = v, tr, true; close(j.done) })
			return []Value{&Union{Tag: 0, Fields: []Value{j}}}
		},
		"Thread.join": func(it *Interp, a []Value, _ callCtx) []Value {
			t := a[0].(*Thread)
			it.heap.dropHandle(t) // the handle is consumed
			it.await(t.done)
			return []Value{t.Result}
		},
		"Join.get": func(it *Interp, a []Value, _ callCtx) []Value {
			// get waits for the thread, never for the scope, which may
			// have ended already
			j := a[0].(*Join)
			it.heap.dropHandle(j) // the handle is consumed
			it.await(j.done)
			j.mu.Lock()
			j.handle = joinTaken
			j.mu.Unlock()
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
			return []Value{&Union{Tag: 0, Fields: []Value{mkInt(tyUsize, addr)}}}
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
			return []Value{&Union{Tag: 0, Fields: []Value{mkInt(tyUsize, addr)}}}
		},
		"SystemAllocator.realloc": func(it *Interp, a []Value, c callCtx) []Value {
			addr, oldSize, size, align := u64(a[1]), u64(a[2]), u64(a[3]), u64(a[4])
			if align == 0 || align&(align-1) != 0 || align > 4096 || size%align != 0 {
				it.trap("explicit", c.pos)
			}
			if !it.heap.is(addr, oldSize, align) {
				it.trap("explicit", c.pos) // not a live allocation of this size and alignment (RF1)
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
			return []Value{&Union{Tag: 0, Fields: []Value{mkInt(tyUsize, n)}}}
		},
		"SystemAllocator.free": func(it *Interp, a []Value, c callCtx) []Value {
			if !it.heap.is(u64(a[1]), u64(a[2]), u64(a[3])) || !it.heap.free(u64(a[1])) {
				it.trap("explicit", c.pos) // not a live allocation of this size and alignment (RF1)
			}
			return nil
		},
		// raw pointers (spec §6) over the byte-addressed heap
		// raw pointers (spec §6): the hot ones are fastIntrinsics
		"ptr_load_volatile": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{it.decode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0))}
		},
		"ptr_store_volatile": func(it *Interp, a []Value, c callCtx) []Value {
			if it.consoleWrite(u64(a[0]), a[1]) {
				return nil
			}
			it.encode(it.rawAtW(u64(a[0]), c.targ(0), c.pos), c.targ(0), a[1])
			return nil
		},
		"ptr_copy": func(it *Interp, a []Value, c callCtx) []Value {
			n := u64(a[2]) * uint64(layout.Of(c.targ(0)).Size)
			dst, ro := it.rawSpan(u64(a[0]), n)
			src, _ := it.rawSpan(u64(a[1]), n)
			if (dst == nil || src == nil) && n > 0 {
				it.trap("explicit", c.pos)
			}
			if ro && n > 0 {
				it.trap("unreachable", c.pos) // a write to memory mapped for reading
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
				it.encode(it.rawAtW(base+uint64(i)*size, t, c.pos), t, s.at(i).get())
			}
			return nil
		},
		// accesses on raw memory (the kern containers' elements): ptr_view
		// and ptr_edit are fastIntrinsics
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
		"ptr_view2": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{&Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}}, &Access{Ref: rawRef{it: it, addr: u64(a[1]), t: c.targ(1)}}}
		},
		"ptr_view_edit": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{&Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}}, &Access{Ref: rawRef{it: it, addr: u64(a[1]), t: c.targ(1)}, Edit: true}}
		},
		// needs_drop: whether destroying a T does anything, as the backend
		// answers it
		"needs_drop": func(_ *Interp, _ []Value, c callCtx) []Value {
			return []Value{Bool(resolve.NeedsDrop(c.targ(0)))}
		},
		"container_identity": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt(tyU32, uint64(it.newIdentity()))}
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
		// swap_at exchanges two elements of a sequence
		"swap_at": func(it *Interp, a []Value, c callCtx) []Value {
			seq := byteSeq(acc(a[0]))
			i, j := u64(a[1]), u64(a[2])
			if i >= uint64(seq.length()) || j >= uint64(seq.length()) {
				it.trap("bounds", c.pos)
			}
			if i != j {
				x, y := seq.at(int(i)), seq.at(int(j))
				xv, yv := x.get(), y.get()
				x.set(yv)
				y.set(xv)
			}
			return nil
		},
		"testing.fail_random": func(it *Interp, a []Value, c callCtx) []Value {
			if !it.TestHooks {
				it.trap("explicit", c.pos)
			}
			it.sh.mu.Lock()
			it.sh.failRandom = int64(u64(a[0])) + 1
			it.sh.mu.Unlock()
			return nil
		},
		"testing.fail_next_spawn": func(it *Interp, a []Value, c callCtx) []Value {
			if !it.TestHooks {
				it.trap("explicit", c.pos)
			}
			it.sh.mu.Lock()
			it.sh.failNextSpawn = true
			it.sh.mu.Unlock()
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
	for k, f := range fastIntrinsics {
		intrinsics[k] = f.slow()
	}
}

// fastIntrinsic is an intrinsic with at most one result (nil for none)
// that keeps no reference to its argument slice after it returns and runs
// no kern code, so no intrinsic call can start while it runs. A compiled
// call passes it a per-thread argument buffer and stores its result
// itself, sparing the argument and result slices of an ordinary
// intrinsic call (specs/interp-alloc).
type fastIntrinsic func(it *Interp, a []Value, c callCtx) Value

// slow is f as an ordinary intrinsic, for every other path (exec, calls by
// name), so both run the same code.
func (f fastIntrinsic) slow() intrinsicFn {
	return func(it *Interp, a []Value, c callCtx) []Value {
		if v := f(it, a, c); v != nil {
			return []Value{v}
		}
		return nil
	}
}

// fastIntrinsics are the raw-pointer core's hot operations (spec §6).
var fastIntrinsics = map[string]fastIntrinsic{
	"ptr_null":      func(it *Interp, a []Value, _ callCtx) Value { return mkInt(tyUsize, 0) },
	"ptr_from_addr": func(it *Interp, a []Value, _ callCtx) Value { return mkInt(tyUsize, u64(a[0])) },
	"ptr_addr":      func(it *Interp, a []Value, _ callCtx) Value { return mkInt(tyUsize, u64(a[0])) },
	"ptr_cast":      func(it *Interp, a []Value, _ callCtx) Value { return mkInt(tyUsize, u64(a[0])) },
	"ptr_offset": func(it *Interp, a []Value, c callCtx) Value {
		n := signedOf(a[1].(Int))
		size := int64(layout.Of(c.targ(0)).Size)
		return mkInt(tyUsize, uint64(int64(u64(a[0]))+n*size))
	},
	"ptr_load": func(it *Interp, a []Value, c callCtx) Value {
		return it.decode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0))
	},
	"ptr_take": func(it *Interp, a []Value, c callCtx) Value {
		return it.decode(it.rawAt(u64(a[0]), c.targ(0), c.pos), c.targ(0))
	},
	"ptr_store": func(it *Interp, a []Value, c callCtx) Value {
		it.encode(it.rawAtW(u64(a[0]), c.targ(0), c.pos), c.targ(0), a[1])
		return nil
	},
	"ptr_view": func(it *Interp, a []Value, c callCtx) Value {
		return &Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}}
	},
	"ptr_edit": func(it *Interp, a []Value, c callCtx) Value {
		return &Access{Ref: rawRef{it: it, addr: u64(a[0]), t: c.targ(0)}, Edit: true}
	},
	"size_of": func(it *Interp, a []Value, c callCtx) Value {
		return mkInt(tyUsize, uint64(layout.Of(c.targ(0)).Size))
	},
	"align_of": func(it *Interp, a []Value, c callCtx) Value {
		return mkInt(tyUsize, uint64(layout.Of(c.targ(0)).Align))
	},
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
			n := newStruct(len(y.Fields))
			for i, f := range y.Fields {
				n.Fields[i] = deepClone(it, f, pos)
			}
			return n
		case *Union:
			n := newUnion(y.Tag, len(y.Fields))
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

// condvarRelease is the first half of a Condvar wait: it checks the guard
// (the edit a Mutex lock bound, held by the calling thread, of the one
// Mutex the Condvar is used with), releases the lock and returns the Mutex
// and the generation channel a notify closes.
func (it *Interp) condvarRelease(a []Value, c callCtx) (*Mutex, chan struct{}) {
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
	return m, gen
}

// condvarReacquire is the second half: the lock is taken again.
func (it *Interp) condvarReacquire(m *Mutex) {
	it.acquire(m)
	m.state.Lock()
	m.Locked = true
	m.owner = it.tid
	m.state.Unlock()
}

// parkOn begins a park on an atomic: under parkMu it reads the atomic and,
// when its value equals expect, returns the generation channel to wait on.
func (it *Interp) parkOn(atomic, expect Value) (chan struct{}, bool) {
	it.sh.parkMu.Lock()
	defer it.sh.parkMu.Unlock()
	atomicMu.Lock()
	cur := atomicLoad(atomic)
	atomicMu.Unlock()
	if !equalValues(cur, expect) {
		return nil, false
	}
	return it.sh.parkGen, true
}

// unpark wakes every parked thread (each re-checks its condition).
func (it *Interp) unpark() {
	it.sh.parkMu.Lock()
	close(it.sh.parkGen)
	it.sh.parkGen = make(chan struct{})
	it.sh.parkMu.Unlock()
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
func (it *Interp) spawned(name string, args []Value, finish func(*Interp, Value, *Trap)) {
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
						if tr = faultTrap(r, it.file); tr == nil {
							panic(r)
						}
					}
					child.trapRecord(tr)
					trapped = tr
				}
			}()
			debug.SetPanicOnFault(true) // as RunEntries: a fault in mapped memory is a trap
			rets := child.callNamed(name, args)
			if len(rets) > 0 {
				result = rets[0]
			}
		}()
		if trapped != nil {
			it.fatalTrap(trapped)
		}
		finish(child, result, trapped)
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
		seq.at(at + i).set(mkInt(tyU8, uint64(b)))
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

// cPath is a kern path as the hosted runtime accepts it: an errno for one
// of 4096 bytes or more (ENAMETOOLONG) or with a NUL (EINVAL), else 0.
func cPath(b []byte) (string, int) {
	if len(b) >= 4096 {
		return "", int(syscall.ENAMETOOLONG)
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", int(syscall.EINVAL)
	}
	return string(b), 0
}

// pathOp runs a system call on a path argument and returns 0 or -errno.
func pathOp(v Value, op func(string) error) []Value {
	p, e := cPath(seqBytes(acc(v)))
	if e == 0 {
		if err := op(p); err != nil {
			e = errnoOf(err)
		}
	}
	return []Value{mkInt(tyI32, uint64(uint32(-e)))}
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
