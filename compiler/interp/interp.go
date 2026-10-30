package interp

import (
	"os"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/bits"
	"sync"
	"sync/atomic"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// Trap is the error raised by a trap (spec §6).
type Trap struct {
	Kind string
	Pos  ast.Pos
	File string
}

func (t *Trap) Error() string { return "trap " + t.Kind }

// Interp executes a module.
type Interp struct {
	mod        *ir.Module
	file       string
	cur        *frame // the running activation, whose file a trap reports
	out        io.Writer
	errOut     io.Writer
	TestHooks  bool
	stackBytes int
	stackUsed  int
	globals    []Value  // this thread's instances of the module's thread_locals
	heap       *rawHeap // byte-addressed memory for raw pointers
	sh         *sharedState
	tid        uint64 // this thread's identifier (0 is the main thread)
	inTrap     bool   // a trap inside the handler terminates
	// Freestanding selects the freestanding emulation: console device
	// writes go to standard output and the halt line ends the run.
	Freestanding bool
}

// New creates an interpreter for a module. out receives program output,
// errOut the trap record.
func New(mod *ir.Module, file string, out, errOut io.Writer) *Interp {
	mu := &sync.Mutex{}
	// the program's arguments default to its name alone, as a native run
	// without arguments reports them (SetArgs replaces them)
	return &Interp{mod: mod, file: file, out: &lockedWriter{mu: mu, w: out}, errOut: &lockedWriter{mu: mu, w: errOut}, stackBytes: 8 << 20, heap: newRawHeap(), sh: &sharedState{nextIdent: 1, fatalCh: make(chan struct{}), args: []string{file}}}
}

// sharedState is the process-wide interpreter state shared by every thread
// (spawned threads are goroutines with their own Interp copy).
type sharedState struct {
	args  []string           // the program's arguments (std/io args)
	files map[int64]*os.File // open files by descriptor (std/io)
	mu            sync.Mutex
	handler       *FuncVal
	nextIdent     uint32
	failNextAlloc bool
	nextTid       uint64
	fatal         atomic.Pointer[Trap]  // the first trap on a spawned thread: the program terminates
	exit          atomic.Pointer[int32] // an exit requested on a spawned thread
	fatalOnce     sync.Once
	fatalCh       chan struct{}
	// stop is set by Stop: every thread ends at its next basic block
	stop atomic.Bool
}

// lockedWriter serializes output from several threads.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// child is a copy of the interpreter for a new thread: fresh thread-locals,
// its own stack budget and identifier, the shared state in common.
func (it *Interp) child() *Interp {
	c := *it
	c.stackUsed = 0
	c.globals = it.freshGlobals()
	c.inTrap = false
	c.cur = nil
	it.sh.mu.Lock()
	it.sh.nextTid++
	c.tid = it.sh.nextTid
	it.sh.mu.Unlock()
	return &c
}

// newIdentity issues a container identity tag (0 when tags are disabled).
func (it *Interp) newIdentity() uint32 {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	id := it.sh.nextIdent
	if id != 0 {
		it.sh.nextIdent++
	}
	return id
}

// runtimeAlloc is the allocation of a runtime object (Shared, Mutex,
// Condvar, a scope, a spawned thread): natively the runtime's first
// allocation for it consumes an armed fail_next_alloc hook and traps oom
// at the call (specs/runtime-trap-file/SPEC.md §2), and so does this.
func (it *Interp) runtimeAlloc(pos ast.Pos) {
	if it.takeFailNextAlloc() {
		it.trap("oom", pos)
	}
}

// takeFailNextAlloc consumes the fail_next_alloc hook.
func (it *Interp) takeFailNextAlloc() bool {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	f := it.sh.failNextAlloc
	it.sh.failNextAlloc = false
	return f
}

// fatalTrap records the first trap on a spawned thread, after its record
// was written on that thread; joins and the end of the program observe it.
func (it *Interp) fatalTrap(tr *Trap) {
	it.sh.fatalOnce.Do(func() {
		it.sh.fatal.Store(tr)
		close(it.sh.fatalCh)
	})
}

// fatalExit records an exit requested on a spawned thread.
func (it *Interp) fatalExit(code exitCode) {
	it.sh.fatalOnce.Do(func() {
		c := int32(code)
		it.sh.exit.Store(&c)
		close(it.sh.fatalCh)
	})
}

// StoppedStatus is the status RunEntry returns for a run Stop ended.
const StoppedStatus = -1

// Stop ends a running program from another goroutine (`kern test`'s
// timeout, specs/test-command/SPEC.md R8): every thread of it ends at its
// next basic block as if it had exited with StoppedStatus, and a thread
// waiting for a join wakes. A thread blocked in a lock or a condition
// wait does not run another block until it is woken, so it may stay
// blocked; it no longer uses the CPU.
func (it *Interp) Stop() {
	it.sh.stop.Store(true)
	it.fatalExit(exitCode(StoppedStatus))
}

// terminated reports whether a spawned thread ended the program, with the
// status: 134 after a trap (its record already written), or the exit code.
func (it *Interp) terminated() (int, bool) {
	select {
	case <-it.sh.fatalCh:
	default:
		return 0, false
	}
	if c := it.sh.exit.Load(); c != nil {
		return int(*c), true
	}
	return 134, true
}

// await waits for a thread to finish, terminating the program when any
// spawned thread trapped or exited.
func (it *Interp) await(done chan struct{}) {
	select {
	case <-done:
	case <-it.sh.fatalCh:
	}
	if code, ok := it.terminated(); ok {
		panic(exitCode(code))
	}
}

// SetIdentityTags disables identity tags (a debug-build feature, PLAN.md
// 2.2) when off: containers get identity 0 and the check is skipped.
// SetArgs sets the program's arguments, the first being the program name,
// as std/io's args() reports them.
func (it *Interp) SetArgs(args []string) { it.sh.args = args }

func (it *Interp) SetIdentityTags(on bool) {
	if !on {
		it.sh.nextIdent = 0
	}
}

// SetStackLimit sets the interpreter's stack budget in bytes; each activation
// is accounted as 512 bytes plus 16 per local and value.
func (it *Interp) SetStackLimit(bytes int) {
	if bytes > 0 {
		it.stackBytes = bytes
	}
}

// Run executes the entry function and returns the process exit status: the
// program's result when it returns an integer, 0 otherwise, 134 on a trap.
// freshGlobals is a new thread's instances of the thread_locals.
func (it *Interp) freshGlobals() []Value {
	gs := make([]Value, len(it.mod.Globals))
	for i, g := range it.mod.Globals {
		gs[i] = globalInit(g)
	}
	return gs
}

func globalInit(g *ir.Global) Value { return constValue(g.Type, g.Init) }

// constValue builds a global's initial value from its folded initializer
// (ir.Global): a scalar, or for an aggregate the []any of its elements or
// fields, with nil the zero value at any depth.
func constValue(t resolve.Type, init any) Value {
	switch v := init.(type) {
	case int64:
		return mkInt(t.(*resolve.Prim).Name, uint64(v))
	case float64:
		return mkFloat(t.(*resolve.Prim).Name, v)
	case bool:
		return Bool(v)
	case []any:
		switch x := t.(type) {
		case *resolve.Array:
			a := &Array{Elems: make([]Value, len(v))}
			for i, el := range v {
				a.Elems[i] = constValue(x.Elem, el)
			}
			return a
		case *resolve.Named:
			args := typeArgs(x)
			s := &Struct{Fields: make([]Value, len(v))}
			for i, f := range x.Decl.Fields {
				s.Fields[i] = constValue(resolve.Subst(f.Type, args), v[i])
			}
			return s
		}
	}
	return zeroOf(t)
}

func (it *Interp) Run() (status int) { return it.RunEntry(it.mod.Entry) }

// RunEntry runs the named function as the program's entry, as Run runs
// main: a test function of `kern test` (specs/test-command/SPEC.md R7).
// Each call starts from fresh globals; an interpreter runs one entry.
func (it *Interp) RunEntry(name string) (status int) {
	it.globals = it.freshGlobals()
	entry := it.mod.Func(name)
	if entry == nil {
		_, _ = fmt.Fprintf(it.errOut, "no entry function\n")
		return 3
	}
	defer func() {
		if r := recover(); r != nil {
			if code, ok := r.(exitCode); ok {
				status = int(code)
				it.halt(status)
				return
			}
			tr, ok := r.(*Trap)
			if !ok {
				panic(r)
			}
			it.trapRecord(tr)
			status = 134
			it.halt(status)
		}
	}()
	rets := it.call(entry, nil)
	if len(rets) == 1 {
		if n, ok := rets[0].(Int); ok {
			status = int(signedOf(n))
		}
	}
	if code, ok := it.terminated(); ok {
		status = code // a spawned thread trapped (record already written) or exited
	}
	it.halt(status)
	return status
}

// halt writes the freestanding completion line (PLAN.md 3.1); hosted runs
// end silently.
func (it *Interp) halt(code int) {
	if it.Freestanding {
		_, _ = fmt.Fprintf(it.out, "{\"halt\":{\"code\":%d}}\n", uint32(int32(code)))
	}
}

// exitCode is raised by the exit intrinsic and recovered by Run.
type exitCode int32

// trapRecordJSON is the default handler's record with the keys in the order
// the C runtimes write them, so an interpreter transcript and a native one
// are byte-identical.
type trapRecordJSON struct {
	Trap struct {
		Kind string `json:"kind"`
		File string `json:"file"`
		Span struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"span"`
		Thread uint64 `json:"thread"`
	} `json:"trap"`
}

// trapRecord writes the trap record (the default handler's output, PLAN.md
// §0) and runs the registered handler once.
func (it *Interp) trapRecord(tr *Trap) {
	var rec trapRecordJSON
	rec.Trap.Kind = tr.Kind
	rec.Trap.File = tr.File
	rec.Trap.Span.Start = tr.Pos.Start
	rec.Trap.Span.End = tr.Pos.End
	rec.Trap.Thread = it.tid
	b, _ := json.Marshal(rec)
	it.sh.mu.Lock()
	handler := it.sh.handler
	it.sh.mu.Unlock()
	if handler == nil || it.inTrap {
		if it.Freestanding {
			_, _ = fmt.Fprintf(it.out, "%s\n", b) // the console is the only device
		} else {
			_, _ = fmt.Fprintf(it.errOut, "%s\n", b)
		}
		return
	}
	// a custom handler: TrapInfo is a struct {kind, file, start, end, thread_id}
	info := &Struct{Fields: []Value{&Union{Tag: trapKindIndex(tr.Kind)}, Int{Ty: "usize"}, mkInt("usize", uint64(tr.Pos.Start)), mkInt("usize", uint64(tr.Pos.End)), mkInt("u64", it.tid)}}
	slot := Value(info)
	h := handler
	it.inTrap = true // a trap inside the handler terminates
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(*Trap); !ok {
					panic(r)
				}
			}
		}()
		it.callNamed(h.Name, []Value{&Access{Ref: slotRef{&slot}}})
	}()
}

var trapKinds = []string{"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "stack", "explicit", "unreachable"}

func trapKindIndex(k string) int {
	for i, n := range trapKinds {
		if n == k {
			return i
		}
	}
	return len(trapKinds) - 1
}

func (it *Interp) trap(kind string, pos ast.Pos) {
	file := it.file
	if it.cur != nil {
		if it.cur.file != "" {
			file = it.cur.file
		}
		if !it.cur.f.Library && it.cur.block != nil && it.cur.block.File != "" {
			file = it.cur.block.File
		}
	}
	panic(&Trap{Kind: kind, Pos: pos, File: file})
}

// frame is one activation.
type frame struct {
	f      *ir.Func
	locals []Value
	vals   []Value
	// file is the one a trap in this frame reports: the function's own, or
	// for a library activation its caller's (with span), as natively.
	file string
	// span is set for a library (std/core) activation: the position of the
	// user-level call that entered the library, which every trap inside
	// reports in place of its own position.
	span *ast.Pos
	// block is the one being executed: a trap in a block the inliner
	// copied from a callee in another file reports that file
	// (specs/cross-file-inlining/SPEC.md R6).
	block *ir.Block
}

// at returns the position a trap at pos reports from this frame.
func (fr *frame) at(pos ast.Pos) ast.Pos {
	if fr.span != nil {
		return *fr.span
	}
	return pos
}

func (it *Interp) callNamed(name string, args []Value) []Value {
	f := it.mod.Func(name)
	if f == nil {
		if fn, ok := intrinsics[name]; ok {
			return fn(it, args, callCtx{})
		}
		panic(fmt.Sprintf("interp: unknown function %s", name))
	}
	return it.call(f, args)
}

// frameSize is the activation's stack budget: the §4.1 size of every local
// plus a fixed cost per value and per frame, so that stack traps are
// differential with native builds for frames dominated by their locals.
func frameSize(f *ir.Func) int {
	n := 64 + 16*f.NumValues
	for _, l := range f.Locals {
		if l.Conv == ast.ConvView || l.Conv == ast.ConvEdit || l.Conv == ast.ConvOut {
			n += layout.Of(&resolve.Access{Elem: l.Type}).Size // an access descriptor, not the referent
			continue
		}
		n += layout.Of(l.Type).Size
	}
	return n
}

func (it *Interp) call(f *ir.Func, args []Value) []Value { return it.callAt(f, args, nil) }

// callAt runs f; span is the call site a library activation reports its
// traps at (nil for user code, and for library code reached without a call
// site, such as a destructor run by drop glue).
func (it *Interp) callAt(f *ir.Func, args []Value, span *ast.Pos) []Value {
	if f.Intrinsic {
		name := intrinsicOf(f.Name)
		if fn, ok := intrinsics[name]; ok {
			return fn(it, args, callCtx{pos: f.Pos})
		}
		panic(fmt.Sprintf("interp: no intrinsic for %s", f.Name))
	}
	frameBytes := frameSize(f)
	fr := &frame{f: f, file: f.File}
	prev := it.cur
	it.cur = fr
	defer func() { it.cur = prev }()
	if f.Library {
		// a library activation reports its caller's span and file; with no
		// call site (drop glue, a function value) an empty span and the
		// program's file, as natively
		fr.file = ""
		if span == nil {
			span = &ast.Pos{}
		} else if prev != nil {
			// the effective file of the caller's current block, as the
			// hidden file argument is natively: a copied block names the
			// callee's file (specs/cross-file-inlining/SPEC.md R6)
			fr.file = prev.file
			if !prev.f.Library && prev.block != nil {
				fr.file = prev.block.FileIn(prev.f)
			}
		}
		fr.span = span
	}
	if it.stackUsed+frameBytes > it.stackBytes {
		it.trap("stack", fr.at(f.Pos))
	}
	it.stackUsed += frameBytes
	defer func() { it.stackUsed -= frameBytes }()
	fr.locals = make([]Value, len(f.Locals))
	fr.vals = make([]Value, f.NumValues)
	for i, p := range f.Params {
		if i < len(args) {
			fr.locals[p.ID] = args[i]
		}
		if p.Conv == ast.ConvOut {
			// the caller's storage exists before the callee writes it
			// (natively, its alloca): an uninitialized local's nil slot is
			// materialized zeroed so that field writes have a place to land
			if a, ok := fr.locals[p.ID].(*Access); ok && a.Ref != nil && a.Ref.get() == nil {
				a.Ref.set(zeroOf(p.Type))
			}
		}
	}
	b := f.Entry
	for {
		if it.sh.stop.Load() {
			panic(exitCode(StoppedStatus))
		}
		fr.block = b
		for _, in := range b.Instrs {
			it.exec(fr, in)
		}
		switch t := b.Term.(type) {
		case *ir.Br:
			b = t.Target
		case *ir.CondBr:
			if fr.vals[t.Cond.ID].(Bool) {
				b = t.Then
			} else {
				b = t.Else
			}
		case *ir.Switch:
			tag := int(fr.vals[t.Tag.ID].(Int).Bits)
			if nb, ok := t.Cases[tag]; ok {
				b = nb
			} else {
				b = t.Default
			}
		case *ir.Ret:
			out := make([]Value, len(t.Vals))
			for i, v := range t.Vals {
				out[i] = fr.vals[v.ID]
			}
			return out
		case *ir.Unreachable:
			it.trap("unreachable", fr.at(f.Pos))
		default:
			panic("interp: block without terminator")
		}
	}
}

// intrinsicOf strips the specialization suffix: "Vec.push[T=u64]" -> "Vec.push".
func intrinsicOf(name string) string {
	for i := 0; i < len(name); i++ {
		if name[i] == '[' {
			return name[:i]
		}
	}
	return name
}

// ---- places ----

// resolve returns the ref and, for sequences, the window of a place.
func (it *Interp) resolve(fr *frame, p ir.Place) (ref, *seqRef) {
	var cur ref
	var seq *seqRef
	if p.Global != nil {
		cur = slotRef{&it.globals[p.Global.ID]}
	} else if p.Local != nil {
		cur = slotRef{&fr.locals[p.Local.ID]}
		// an access parameter's slot holds the caller's access: places rooted
		// at it read and write the referent, as the backend dereferences the
		// slot. An out parameter is an edit access to the caller's storage.
		if a, ok := fr.locals[p.Local.ID].(*Access); ok && (p.Local.Conv == ast.ConvView || p.Local.Conv == ast.ConvEdit || p.Local.Conv == ast.ConvOut) {
			cur, seq = a.Ref, a.Seq
		}
	} else {
		a := fr.vals[p.Access.ID].(*Access)
		cur, seq = a.Ref, a.Seq
	}
	// arrayWindow opens the whole array at cur as a sequence window
	arrayWindow := func() *seqRef {
		if rr, ok := cur.(rawRef); ok {
			return rr.elems()
		}
		arr := cur.get().(*Array)
		return &seqRef{elems: func() []Value { return arr.Elems }, lo: 0, hi: len(arr.Elems)}
	}
	for _, e := range p.Path {
		switch e.Kind {
		case ir.PathField:
			if rr, ok := cur.(rawRef); ok {
				cur = rr.field(e.Field)
			} else {
				s := cur.get().(*Struct)
				cur = idxRef{elems: func() []Value { return s.Fields }, i: e.Field}
			}
			seq = nil
		case ir.PathVariant:
			if rr, ok := cur.(rawRef); ok {
				cur = rr.variantField(e.Tag, e.Field)
			} else {
				u := cur.get().(*Union)
				cur = idxRef{elems: func() []Value { return u.Fields }, i: e.Field}
			}
			seq = nil
		case ir.PathIndex:
			i := int(fr.vals[e.Index.ID].(Int).Bits)
			if seq == nil {
				seq = arrayWindow()
			}
			cur = seq.at(i)
			seq = nil
		case ir.PathSlice:
			lo := int(fr.vals[e.Lo.ID].(Int).Bits)
			hi := int(fr.vals[e.Hi.ID].(Int).Bits)
			if seq == nil {
				seq = arrayWindow()
			}
			seq = &seqRef{elems: seq.elems, raw: seq.raw, lo: seq.lo + lo, hi: seq.lo + hi}
			cur = nil
		}
	}
	return cur, seq
}

func (it *Interp) length(fr *frame, p ir.Place) int {
	cur, seq := it.resolve(fr, p)
	if seq != nil {
		return seq.length()
	}
	if arr, ok := cur.get().(*Array); ok {
		return len(arr.Elems)
	}
	panic("interp: len of non-sequence")
}

// ---- instructions ----

func (it *Interp) exec(fr *frame, in ir.Instr) {
	switch i := in.(type) {
	case *ir.Const:
		fr.vals[i.Dst.ID] = it.constValue(i)
	case *ir.Load:
		r, seq := it.resolve(fr, i.Place)
		if seq != nil {
			fr.vals[i.Dst.ID] = &Access{Seq: seq}
			return
		}
		fr.vals[i.Dst.ID] = copyValue(r.get())
	case *ir.Store:
		r, _ := it.resolve(fr, i.Place)
		r.set(fr.vals[i.Src.ID])
	case *ir.Move:
		r, _ := it.resolve(fr, i.Src)
		fr.vals[i.Dst.ID] = r.get()
		r.set(nil)
	case *ir.Drop:
		r, _ := it.resolve(fr, i.Place)
		it.destroy(r.get(), i.Type)
		r.set(nil)
	case *ir.AccessBegin:
		r, seq := it.resolve(fr, i.Place)
		fr.vals[i.Dst.ID] = &Access{Ref: r, Seq: seq, Edit: i.Edit}
	case *ir.AccessEnd:
		if i.Release != "" {
			it.callNamed(i.Release, []Value{fr.vals[i.Recv.ID]})
		}
	case *ir.Call:
		args := make([]Value, len(i.Args))
		for k, a := range i.Args {
			args[k] = fr.vals[a.ID]
		}
		var rets []Value
		switch {
		case i.Intrinsic != "":
			fn, ok := intrinsics[i.Intrinsic]
			if !ok {
				panic(fmt.Sprintf("interp: unknown intrinsic %s", i.Intrinsic))
			}
			rets = fn(it, args, callCtx{pos: fr.at(i.Pos), targs: i.TypeArgs})
		case i.Fn != nil:
			switch fv := fr.vals[i.Fn.ID].(type) {
			case ClosureVal:
				rets = it.callNamed(fv.Name, append(append([]Value(nil), fv.Env...), args...))
			default:
				rets = it.callNamed(fv.(FuncVal).Name, args)
			}
		default:
			if callee := it.mod.Func(i.Callee); callee != nil && callee.Library {
				// entering (or staying in) the library: traps report this call
				pos := fr.at(i.Pos)
				rets = it.callAt(callee, args, &pos)
			} else {
				rets = it.callNamed(i.Callee, args)
			}
		}
		for k, d := range i.Dsts {
			if k < len(rets) {
				fr.vals[d.ID] = rets[k]
			}
		}
	case *ir.Binary:
		fr.vals[i.Dst.ID] = it.binary(i.Op, fr.vals[i.L.ID], fr.vals[i.R.ID], i.Mode)
	case *ir.Unary:
		x := fr.vals[i.X.ID]
		switch i.Op {
		case "!":
			fr.vals[i.Dst.ID] = !x.(Bool)
		case "ctz", "clz", "popcount":
			fr.vals[i.Dst.ID] = bitCount(i.Op, x.(Int))
		default:
			switch v := x.(type) {
			case Int:
				fr.vals[i.Dst.ID] = mkInt(v.Ty, -v.Bits)
			case Float:
				fr.vals[i.Dst.ID] = mkFloat(v.Ty, -v.V)
			}
		}
	case *ir.Conv:
		fr.vals[i.Dst.ID] = it.conv(fr.vals[i.X.ID], i.To, i.Truncate, i.Saturate)
	case *ir.CheckOverflow:
		it.checkOverflow(i.Op, fr.vals[i.L.ID], fr.vals[i.R.ID], fr.at(i.Pos))
	case *ir.CheckBounds:
		ln := fr.vals[i.Len.ID].(Int).Bits
		if i.Index != nil {
			if fr.vals[i.Index.ID].(Int).Bits >= ln {
				it.trap("bounds", fr.at(i.Pos))
			}
			return
		}
		lo := fr.vals[i.Lo.ID].(Int).Bits
		hi := fr.vals[i.Hi.ID].(Int).Bits
		if lo > hi || hi > ln {
			it.trap("bounds", fr.at(i.Pos))
		}
	case *ir.CheckConv:
		it.checkConv(fr.vals[i.X.ID], i.To, fr.at(i.Pos))
	case *ir.Len:
		fr.vals[i.Dst.ID] = mkInt("usize", uint64(it.length(fr, i.Place)))
	case *ir.MakeStruct:
		s := &Struct{Fields: make([]Value, len(i.Fields))}
		for k, f := range i.Fields {
			s.Fields[k] = fr.vals[f.ID]
		}
		fr.vals[i.Dst.ID] = s
	case *ir.MakeUnion:
		u := &Union{Tag: i.Tag, Fields: make([]Value, len(i.Fields))}
		for k, f := range i.Fields {
			u.Fields[k] = fr.vals[f.ID]
		}
		fr.vals[i.Dst.ID] = u
	case *ir.MakeArray:
		a := &Array{Elems: make([]Value, len(i.Elems))}
		for k, e := range i.Elems {
			a.Elems[k] = fr.vals[e.ID]
		}
		fr.vals[i.Dst.ID] = a
	case *ir.Zero:
		fr.vals[i.Dst.ID] = zeroOf(i.Dst.Type)
	case *ir.Tag:
		r, _ := it.resolve(fr, i.Place)
		fr.vals[i.Dst.ID] = mkInt("u32", uint64(r.get().(*Union).Tag))
	case *ir.FuncRef:
		fr.vals[i.Dst.ID] = FuncVal{Name: i.Name}
	case *ir.MakeClosure:
		env := make([]Value, len(i.Captures))
		for k, c := range i.Captures {
			env[k] = fr.vals[c.ID]
		}
		fr.vals[i.Dst.ID] = ClosureVal{Name: i.Name, Env: env}
	case *ir.Trap:
		it.trap(i.Kind, fr.at(i.Pos))
	case *ir.Copy:
		fr.vals[i.Dst.ID] = copyValue(fr.vals[i.Src.ID])
	case *ir.UnsafeBegin, *ir.UnsafeEnd, *ir.Init:
	default:
		panic(fmt.Sprintf("interp: unsupported instruction %T", in))
	}
}

func (it *Interp) constValue(c *ir.Const) Value {
	switch {
	case c.Unit:
		return Unit{}
	case c.Bytes != nil:
		elems := make([]Value, len(c.Bytes))
		for i, b := range c.Bytes {
			elems[i] = mkInt("u8", uint64(b))
		}
		arr := &Array{Elems: elems}
		return &Access{Seq: &seqRef{elems: func() []Value { return arr.Elems }, lo: 0, hi: len(elems)}}
	}
	switch t := c.Dst.Type.(type) {
	case *resolve.Prim:
		switch t.Name {
		case "bool":
			return Bool(c.Bool)
		case "f32", "f64":
			return mkFloat(t.Name, c.Float)
		}
		v := c.Int
		if c.Neg {
			v = -v
		}
		return mkInt(t.Name, v)
	}
	return Unit{}
}

// ---- arithmetic (spec §4.1) ----

// shiftCount is the shift count the operation uses: in wrapping mode the
// count masked to width - 1 (spec §4.1); otherwise the count as given, which
// the preceding check bounded below the width.
func shiftCount(n uint64, ty string, mode string) uint64 {
	if mode == "wrapping" {
		return n & uint64(width(ty)-1)
	}
	return n & 63
}

func (it *Interp) checkOverflow(op string, l, r Value, pos ast.Pos) {
	a, ok := l.(Int)
	if !ok {
		return // floats never trap
	}
	if kind := arithTrap(op, a, r.(Int)); kind != "" {
		it.trap(kind, pos)
	}
}

// arithTrap is the kind of trap the trapping form of op on a and b takes,
// or "" when it completes (spec §4.1): div_zero, overflow (including the
// minimum signed value divided by -1 and a set bit shifted out), shift (a
// count of the width or more).
func arithTrap(op string, a, b Int) string {
	ty := a.Ty
	w := width(ty)
	switch op {
	case "/", "%":
		if b.Bits == 0 {
			return "div_zero"
		}
		if signed(ty) && signedOf(a) == minSigned(w) && signedOf(b) == -1 {
			return "overflow"
		}
	case "<<", ">>":
		if b.Bits >= uint64(w) {
			return "shift"
		}
		if op == "<<" {
			shifted := mask(ty, a.Bits<<b.Bits) >> b.Bits
			if shifted != a.Bits {
				return "overflow"
			}
		}
	case "+", "-", "*":
		if signed(ty) {
			x, y := signedOf(a), signedOf(b)
			var res int64
			var over bool
			switch op {
			case "+":
				res = x + y
				over = (x > 0 && y > 0 && res < 0) || (x < 0 && y < 0 && res >= 0)
			case "-":
				res = x - y
				over = (x >= 0 && y < 0 && res < 0) || (x < 0 && y > 0 && res >= 0)
			case "*":
				if x != 0 && y != 0 {
					res = x * y
					over = res/y != x || (x == -1 && y == math.MinInt64) || (y == -1 && x == math.MinInt64)
				}
			}
			if !over && w < 64 && (res < minSigned(w) || res > maxSigned(w)) {
				over = true
			}
			if over {
				return "overflow"
			}
			return ""
		}
		x, y := a.Bits, b.Bits
		var over bool
		switch op {
		case "+":
			s, c := bits.Add64(x, y, 0)
			over = c != 0 || mask(ty, s) != s
		case "-":
			over = y > x
		case "*":
			hi, lo := bits.Mul64(x, y)
			over = hi != 0 || mask(ty, lo) != lo
		}
		if over {
			return "overflow"
		}
	}
	return ""
}

func minSigned(w uint) int64 { return -(1 << (w - 1)) }
func maxSigned(w uint) int64 { return 1<<(w-1) - 1 }

func (it *Interp) binary(op string, l, r Value, mode string) Value {
	switch a := l.(type) {
	case Bool:
		b := r.(Bool)
		switch op {
		case "==":
			return Bool(a == b)
		case "!=":
			return Bool(a != b)
		case "&&":
			return a && b
		case "||":
			return a || b
		}
	case Float:
		b := r.(Float)
		switch op {
		case "+":
			return mkFloat(a.Ty, a.V+b.V)
		case "-":
			return mkFloat(a.Ty, a.V-b.V)
		case "*":
			return mkFloat(a.Ty, a.V*b.V)
		case "/":
			return mkFloat(a.Ty, a.V/b.V)
		case "==":
			return Bool(a.V == b.V)
		case "!=":
			return Bool(a.V != b.V)
		case "<":
			return Bool(a.V < b.V)
		case "<=":
			return Bool(a.V <= b.V)
		case ">":
			return Bool(a.V > b.V)
		case ">=":
			return Bool(a.V >= b.V)
		}
	case Int:
		b := r.(Int)
		ty := a.Ty
		if signed(ty) {
			x, y := signedOf(a), signedOf(b)
			switch op {
			case "+":
				return mkInt(ty, uint64(x+y))
			case "-":
				return mkInt(ty, uint64(x-y))
			case "*":
				return mkInt(ty, uint64(x*y))
			case "/":
				return mkInt(ty, uint64(x/y))
			case "%":
				return mkInt(ty, uint64(x%y))
			case "<<":
				return mkInt(ty, uint64(x<<shiftCount(b.Bits, ty, mode)))
			case ">>":
				return mkInt(ty, uint64(x>>shiftCount(b.Bits, ty, mode)))
			case "&":
				return mkInt(ty, a.Bits&b.Bits)
			case "|":
				return mkInt(ty, a.Bits|b.Bits)
			case "^":
				return mkInt(ty, a.Bits^b.Bits)
			case "==":
				return Bool(x == y)
			case "!=":
				return Bool(x != y)
			case "<":
				return Bool(x < y)
			case "<=":
				return Bool(x <= y)
			case ">":
				return Bool(x > y)
			case ">=":
				return Bool(x >= y)
			}
		}
		x, y := a.Bits, b.Bits
		switch op {
		case "+":
			return mkInt(ty, x+y)
		case "-":
			return mkInt(ty, x-y)
		case "*":
			return mkInt(ty, x*y)
		case "/":
			return mkInt(ty, x/y)
		case "%":
			return mkInt(ty, x%y)
		case "<<":
			return mkInt(ty, x<<shiftCount(y, ty, mode))
		case ">>":
			return mkInt(ty, x>>shiftCount(y, ty, mode))
		case "&":
			return mkInt(ty, x&y)
		case "|":
			return mkInt(ty, x|y)
		case "^":
			return mkInt(ty, x^y)
		case "==":
			return Bool(x == y)
		case "!=":
			return Bool(x != y)
		case "<":
			return Bool(x < y)
		case "<=":
			return Bool(x <= y)
		case ">":
			return Bool(x > y)
		case ">=":
			return Bool(x >= y)
		}
	case *Union:
		b := r.(*Union)
		switch op {
		case "==":
			return Bool(equalValues(a, b))
		case "!=":
			return Bool(!equalValues(a, b))
		}
	case *Struct:
		b := r.(*Struct)
		switch op {
		case "==":
			return Bool(equalValues(a, b))
		case "!=":
			return Bool(!equalValues(a, b))
		}
	}
	panic(fmt.Sprintf("interp: unsupported binary %s on %T", op, l))
}

func equalValues(a, b Value) bool {
	switch x := a.(type) {
	case Int:
		return x.Bits == b.(Int).Bits
	case Float:
		return x.V == b.(Float).V
	case Bool:
		return x == b.(Bool)
	case Unit:
		return true
	case *Struct:
		y := b.(*Struct)
		for i := range x.Fields {
			if !equalValues(x.Fields[i], y.Fields[i]) {
				return false
			}
		}
		return true
	case *Union:
		y := b.(*Union)
		if x.Tag != y.Tag {
			return false
		}
		for i := range x.Fields {
			if !equalValues(x.Fields[i], y.Fields[i]) {
				return false
			}
		}
		return true
	case *Array:
		y := b.(*Array)
		for i := range x.Elems {
			if !equalValues(x.Elems[i], y.Elems[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func (it *Interp) checkConv(x Value, to string, pos ast.Pos) {
	switch v := x.(type) {
	case Int:
		if to == "f32" || to == "f64" {
			return
		}
		var val int64
		var big bool
		if signed(v.Ty) {
			val = signedOf(v)
		} else if v.Bits > math.MaxInt64 {
			big = true
		} else {
			val = int64(v.Bits)
		}
		w := width(to)
		if signed(to) {
			if big || val < minSigned(w) || val > maxSigned(w) {
				it.trap("conversion", pos)
			}
			return
		}
		if val < 0 {
			it.trap("conversion", pos)
		}
		if w < 64 && (big || uint64(val) > (1<<w)-1) {
			it.trap("conversion", pos)
		}
	case Float:
		if to == "f32" || to == "f64" {
			return
		}
		if math.IsNaN(v.V) || math.IsInf(v.V, 0) {
			it.trap("conversion", pos)
		}
		t := math.Trunc(v.V)
		w := width(to)
		if signed(to) {
			if t < -math.Ldexp(1, int(w)-1) || t >= math.Ldexp(1, int(w)-1) {
				it.trap("conversion", pos)
			}
		} else if t < 0 || t >= math.Ldexp(1, int(w)) {
			it.trap("conversion", pos)
		}
	}
}

func (it *Interp) conv(x Value, to string, truncate, saturate bool) Value {
	switch v := x.(type) {
	case Int:
		if to == "f32" || to == "f64" {
			if signed(v.Ty) {
				return mkFloat(to, float64(signedOf(v)))
			}
			return mkFloat(to, float64(v.Bits))
		}
		bitsv := v.Bits
		if signed(v.Ty) {
			bitsv = uint64(signedOf(v)) // sign-extend to 64 bits
		}
		return mkInt(to, bitsv)
	case Float:
		if to == "f32" || to == "f64" {
			return mkFloat(to, v.V)
		}
		t := math.Trunc(v.V)
		w := width(to)
		if saturate {
			if math.IsNaN(t) {
				return Int{Ty: to}
			}
			if signed(to) {
				if t < -math.Ldexp(1, int(w)-1) {
					return mkInt(to, uint64(minSigned(w)))
				}
				if t >= math.Ldexp(1, int(w)-1) {
					return mkInt(to, uint64(maxSigned(w)))
				}
			} else if t < 0 {
				return Int{Ty: to}
			} else if t >= math.Ldexp(1, int(w)) {
				return mkInt(to, math.MaxUint64)
			}
		}
		if signed(to) {
			return mkInt(to, uint64(int64(t)))
		}
		return mkInt(to, uint64(t))
	}
	return x
}

// ---- destruction (spec §5.7) ----

// destroy runs a value's destructor and destroys its fields in reverse
// declaration order.
func (it *Interp) destroy(v Value, t resolve.Type) {
	if v == nil || resolve.IsCopy(t) {
		return
	}
	switch x := t.(type) {
	case *resolve.Array:
		arr, ok := v.(*Array)
		if !ok {
			return
		}
		for i := len(arr.Elems) - 1; i >= 0; i-- {
			it.destroy(arr.Elems[i], x.Elem)
		}
	case *resolve.Named:
		if layout.Opaque(x) {
			it.destroyLibrary(v, x)
			return
		}
		args := typeArgs(x)
		if x.Decl.Destructor != nil {
			slot := v
			it.callNamed(x.String()+".destructor", []Value{&Access{Ref: slotRef{&slot}, Edit: true}})
			v = slot
		}
		switch val := v.(type) {
		case *Struct:
			for i := len(x.Decl.Fields) - 1; i >= 0; i-- {
				it.destroy(val.Fields[i], resolve.Subst(x.Decl.Fields[i].Type, args))
			}
		case *Union:
			vd := x.Decl.Variants[val.Tag]
			for i := len(vd.Fields) - 1; i >= 0; i-- {
				it.destroy(val.Fields[i], resolve.Subst(vd.Fields[i].Type, args))
			}
		}
	}
}

func (it *Interp) destroyLibrary(v Value, n *resolve.Named) {
	elem := func(i int) resolve.Type {
		if i < len(n.Args) {
			return n.Args[i]
		}
		return &resolve.Unit{}
	}
	switch x := v.(type) {
	case *Shared:
		x.Count--
		if x.Count == 0 {
			it.destroy(x.Val, elem(0))
		}
	case *Mutex:
		it.destroy(x.Val, elem(0))
	case *Condvar:
		// nothing is owned: waiters, if any, hold their own reference
	case *Scope:
		// the scope's end joins every thread it spawned
		for _, j := range x.Joins {
			it.await(j.done)
			if !j.Taken {
				j.Taken = true
				it.destroy(j.Result, elem(0))
			}
		}
	case *Thread:
		// detached: the thread runs on; its result is leaked (as natively)
	case *Join:
		if !x.Taken {
			it.await(x.done)
			x.Taken = true
			it.destroy(x.Result, elem(0))
		}
	}
}

// bitCount evaluates the bit-count unary operations on an integer of its
// declared width: trailing or leading zeros (the width for a zero operand)
// or the number of set bits, as a u32.
func bitCount(op string, v Int) Int {
	w := width(v.Ty)
	var n int
	switch op {
	case "ctz":
		n = bits.TrailingZeros64(v.Bits)
		if n > int(w) {
			n = int(w)
		}
	case "clz":
		n = bits.LeadingZeros64(v.Bits) - (64 - int(w))
	default:
		n = bits.OnesCount64(v.Bits)
	}
	return mkInt("u32", uint64(n))
}
