package interp

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

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
	// frames are the trapping thread's activations when the trap was
	// raised, outermost first, kept only when a backtrace was asked for
	// (the panic that carries the trap unwinds them)
	frames []*ir.Func
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
	spawnStack int // the configured budget of spawned threads (SetStackLimit)
	// backtrace: the default trap handler writes a backtrace after the
	// record (specs/runtime RE2); frames is then this thread's activations,
	// outermost first, kept only while it is on
	backtrace bool
	// ownsProcess: see SetOwnsProcess
	ownsProcess bool
	frames      []*frame
	globals     []Value  // this thread's instances of the module's thread_locals
	heap        *rawHeap // byte-addressed memory for raw pointers
	sh          *sharedState
	tid         uint64 // this thread's identifier (0 is the main thread)
	inTrap      bool   // a trap inside the handler terminates
	// Freestanding selects the freestanding emulation: console device
	// writes go to standard output and the halt line ends the run.
	Freestanding bool
	// genericExec runs every instruction through exec instead of the
	// compiled form: the reference the compiled form is tested against
	// (specs/interp-compile).
	genericExec bool
	// plans caches destroy's per-type work for this thread
	// (specs/interp-destroy-plans); a child thread starts its own.
	plans map[resolve.Type]*destroyPlan
	// argBuf carries a direct call's arguments to callAt, which copies
	// them into the callee's locals before anything else runs, so one
	// buffer per thread serves every call, recursive ones included
	// (specs/interp-call-path); a child thread starts its own.
	argBuf []Value
	// intrBuf carries a compiled call's arguments to a fastIntrinsic, which
	// keeps no reference to it and runs no kern code; the call clears it
	// when the intrinsic returns (one trapping leaves its arguments until
	// the next call, as argBuf does)
	intrBuf []Value
}

// New creates an interpreter for a module. out receives program output,
// errOut the trap record.
func New(mod *ir.Module, file string, out, errOut io.Writer) *Interp {
	mu := &sync.Mutex{}
	// the program's arguments default to its name alone, as a native run
	// without arguments reports them (SetArgs replaces them)
	return &Interp{mod: mod, file: file, out: &lockedWriter{mu: mu, w: out}, errOut: &lockedWriter{mu: mu, w: errOut}, stackBytes: 8 << 20, spawnStack: 8 << 20, heap: newRawHeap(), sh: &sharedState{nextIdent: 1, fatalCh: make(chan struct{}), parkGen: make(chan struct{}), args: []string{file}}}
}

// sharedState is the process-wide interpreter state shared by every thread
// (spawned threads are goroutines with their own Interp copy).
type sharedState struct {
	args          []string           // the program's arguments (std/io args)
	files         map[int64]*os.File // open files by descriptor (std/io)
	socks         map[int64]*sock    // open sockets by descriptor (std/net)
	sigSock       int64              // the descriptor that stands for the signal pipe's read end
	sigSockOnce   sync.Once
	fileOpts      map[int64]uint32    // the OpenOptions bits each open file was opened with
	maps          map[uint64]*fileMap // file mappings by address (std/mem)
	nextSock      int64
	procs         map[int64]*procEntry // started children by handle (std/process)
	procsKilled   bool                 // KillChildren ran: a child registered later is killed at once
	procFaults    [3]atomic.Int32      // the std/process fault hook's slots, by pipe kind
	nextProc      int64
	mu            sync.Mutex
	handler       *FuncVal
	nextIdent     uint32
	failNextAlloc bool
	failNextSpawn bool  // testing.fail_next_spawn
	failRandom    int64 // testing.fail_random: the request of that number fails; 0 when not armed
	nextTid       uint64
	fatal         atomic.Pointer[Trap]  // the first trap on a spawned thread: the program terminates
	exit          atomic.Pointer[int32] // an exit requested on a spawned thread
	fatalOnce     sync.Once
	fatalCh       chan struct{}
	// stop is set by Stop: every thread ends at its next basic block
	stop atomic.Bool
	// stackAPI is thread.set_stack_bytes's value, 0 when never set: the
	// simulated stack of every thread spawned afterwards
	stackAPI atomic.Uint64
	// parkMu and parkGen are the parking of specs/runtime RD1: a parker
	// checks its atomic under parkMu and then waits for the generation
	// channel it read there to be closed; every unpark closes the current
	// one and makes the next. A waker changes the atomic before it
	// unparks, so a parker either sees the new value or holds the channel
	// the waker closes: no wake is lost. Every unpark wakes every parker,
	// which the contract permits (a parker re-checks its condition).
	parkMu  sync.Mutex
	parkGen chan struct{}
	// funcs holds each called function's funcInfo
	funcs sync.Map
}

// lockedWriter serializes output from several threads and remembers the
// first failed write, which io.flush_stdout reports (specs/runtime RB3).
type lockedWriter struct {
	mu  *sync.Mutex
	w   io.Writer
	err error
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.w.Write(b)
	if err != nil && l.err == nil {
		l.err = err
	}
	return n, err
}

// takeErr returns and clears the remembered failure.
func (l *lockedWriter) takeErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.err
	l.err = nil
	return err
}

// isTerminal reports whether w, or the writer a lockedWriter wraps, is a
// terminal device.
func isTerminal(w any) bool {
	if l, ok := w.(*lockedWriter); ok {
		w = l.w
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty(f)
}

// isatty asks the system whether f is a terminal, as isatty(3) does: by
// reading its terminal settings.
func isatty(f *os.File) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	var termios [256]byte // room for a struct termios on every supported system
	var e syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(ioctlGetTermios), uintptr(unsafe.Pointer(&termios[0])))
	}); err != nil {
		return false
	}
	return e == 0
}

// child is a copy of the interpreter for a new thread: fresh thread-locals,
// its own stack budget and identifier, the shared state in common.
func (it *Interp) child() *Interp {
	c := *it
	c.stackUsed = 0
	c.stackBytes = int(it.nextStackBytes())
	c.globals = it.freshGlobals()
	c.inTrap = false
	c.cur = nil
	c.plans = nil
	c.frames = nil
	c.argBuf = nil
	c.intrBuf = nil
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

// takeFailNextSpawn consumes testing.fail_next_spawn's flag.
func (it *Interp) takeFailNextSpawn() bool {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	f := it.sh.failNextSpawn
	it.sh.failNextSpawn = false
	return f
}

// spawnErrValue is a spawn's Err(SpawnErr.ResourceExhausted).
func spawnErrValue() []Value {
	return []Value{&Union{Tag: 1, Fields: []Value{&Union{Tag: 0}}}}
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
// waiting for a join, a lock or a condition variable wakes and ends the
// same way.
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

// awaitFor is await with a time limit: it reports false when ns
// nanoseconds passed before done was closed.
func (it *Interp) awaitFor(done chan struct{}, ns uint64) bool {
	const maxWait = uint64(1<<63 - 1)
	if ns > maxWait {
		ns = maxWait
	}
	timer := time.NewTimer(time.Duration(ns))
	defer timer.Stop()
	woken := true
	select {
	case <-done:
	case <-timer.C:
		woken = false
	case <-it.sh.fatalCh:
	}
	if code, ok := it.terminated(); ok {
		panic(exitCode(code))
	}
	return woken
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
// is accounted as 512 bytes plus 16 per local and value. It is the budget
// of the thread that runs main and of every spawned thread, until the
// program calls thread.set_stack_bytes (specs/runtime RC2).
func (it *Interp) SetStackLimit(bytes int) {
	if bytes > 0 {
		it.stackBytes = bytes
		it.spawnStack = bytes
	}
}

// The bounds of thread.set_stack_bytes and of the configured stack size,
// as in the hosted runtime (KERN_STACK_MIN, KERN_STACK_MAX).
const (
	stackBytesMin = 65536
	stackBytesMax = 1 << 30
)

// nextStackBytes is the stack size the next spawned thread gets: the
// program's own setting, else the configured budget, rounded up to a whole
// page as natively.
func (it *Interp) nextStackBytes() uint64 {
	n := it.sh.stackAPI.Load()
	if n == 0 {
		n = uint64(it.spawnStack)
	}
	pg := uint64(os.Getpagesize())
	return (n + pg - 1) / pg * pg
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
		return mkInt(tyOf(t.(*resolve.Prim).Name), uint64(v))
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
	return it.RunEntries([]string{name})
}

// RunEntries runs functions one after another as one program, sharing its
// thread_locals: a test's setup, the test and its teardown (`kern test`,
// specs/test-runs R2). A trap, an exit or a terminating spawned thread
// ends the sequence; the status is RunEntry's for the last function run.
func (it *Interp) RunEntries(names []string) (status int) {
	it.globals = it.freshGlobals()
	// a program that traps or exits runs no destructor: what it did to
	// the process's signals ends with it all the same
	defer releaseSignals()
	if it.ownsProcess {
		defaultSignals()
	}
	// a fault in memory the system mapped is a panic to recover, not the
	// end of the process
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	var entries []*ir.Func
	for _, name := range names {
		entry := it.mod.Func(name)
		if entry == nil {
			_, _ = fmt.Fprintf(it.errOut, "no entry function\n")
			return 3
		}
		entries = append(entries, entry)
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
				if tr = faultTrap(r, it.file); tr == nil {
					panic(r)
				}
			}
			it.trapRecord(tr)
			status = 134
			it.halt(status)
		}
	}()
	for _, entry := range entries {
		status = 0
		rets := it.call(entry, nil)
		if len(rets) == 1 {
			if n, ok := rets[0].(Int); ok {
				status = int(signedOf(n))
			}
		}
		if code, ok := it.terminated(); ok {
			status = code // a spawned thread trapped (record already written) or exited
			break
		}
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
		Line   int    `json:"line"`
		Col    int    `json:"col"`
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
	rec.Trap.Line, rec.Trap.Col = it.mod.LineCol(tr.File, tr.Pos.Start, tr.Pos.End)
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
			if it.backtrace {
				_, _ = fmt.Fprintf(it.errOut, "%s\n", it.backtraceLine(tr))
			}
		}
		return
	}
	// a custom handler: TrapInfo is a struct {kind, file, start, end, thread_id}
	info := &Struct{Fields: []Value{&Union{Tag: trapKindIndex(tr.Kind)}, Int{Ty: tyUsize}, mkInt(tyUsize, uint64(tr.Pos.Start)), mkInt(tyUsize, uint64(tr.Pos.End)), mkInt(tyU64, it.tid)}}
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

// SetBacktrace makes the default trap handler write a backtrace after the
// trap record, as a native program does with KERN_BACKTRACE=1 or built with
// --backtrace (specs/runtime RE2).
func (it *Interp) SetBacktrace(on bool) { it.backtrace = on }

// SetOwnsProcess tells the interpreter that the program it runs is all
// this process is for (`kern run`): a signal whose default in the system
// ends a program then ends this one as the system would (specs/runtime
// RM6), where a program run inside another (a test) leaves the process's
// signals to its host.
func (it *Interp) SetOwnsProcess(on bool) { it.ownsProcess = on }

// faultTrap is the trap of a memory fault that the Go runtime raised as a
// panic: an access to a mapped file's bytes past the file's end. It is
// kind unreachable with no location, which is how the runtime reports a
// fault that is not a stack overflow. Any other panic is not one.
func faultTrap(r any, file string) *Trap {
	if _, ok := r.(interface {
		error
		Addr() uintptr
	}); ok {
		return &Trap{Kind: "unreachable", File: file}
	}
	return nil
}

// backtraceLine is the backtrace of the trapping thread: its user
// functions' activations, innermost first, at most 64, each with the
// function's qualified name and its declaration's file and line. Library
// activations are left out, as natively.
func (it *Interp) backtraceLine(tr *Trap) []byte {
	type btFrame struct {
		Function string `json:"function"`
		File     string `json:"file"`
		Line     int    `json:"line"`
	}
	var rec struct {
		Backtrace struct {
			Thread    uint64    `json:"thread"`
			Frames    []btFrame `json:"frames"`
			Truncated bool      `json:"truncated,omitempty"`
		} `json:"backtrace"`
	}
	rec.Backtrace.Thread = it.tid
	rec.Backtrace.Frames = []btFrame{}
	for i := len(tr.frames) - 1; i >= 0; i-- {
		f := tr.frames[i]
		if f.Library {
			continue
		}
		if len(rec.Backtrace.Frames) == 64 {
			rec.Backtrace.Truncated = true
			break
		}
		line, _ := it.mod.LineCol(f.File, f.Pos.Start, f.Pos.End)
		rec.Backtrace.Frames = append(rec.Backtrace.Frames, btFrame{Function: f.Name, File: f.File, Line: line})
	}
	b, _ := json.Marshal(rec)
	return b
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
	tr := &Trap{Kind: kind, Pos: pos, File: file}
	if it.backtrace {
		tr.frames = make([]*ir.Func, len(it.frames))
		for i, fr := range it.frames {
			tr.frames[i] = fr.f
		}
	}
	panic(tr)
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
	// reports in place of its own position. It is never written through,
	// so call sites pass stable pointers (spanAt) rather than a copy that
	// would cost an allocation per library call.
	span *ast.Pos
	// block is the one being executed: a trap in a block the inliner
	// copied from a callee in another file reports that file
	// (specs/cross-file-inlining/SPEC.md R6).
	block *ir.Block
	info  *funcInfo
	// retTo and retIDs, set by a direct call, are where its return values
	// go: the caller's frame and value slots, written by the return
	// itself instead of through a slice (specs/interp-call-path)
	retTo  *frame
	retIDs []int
}

// smallSlots is the most locals and values an activation keeps inline with
// its frame; smallFrame is that frame and its slots in one object.
const smallSlots = 14

type smallFrame struct {
	fr    frame
	slots [smallSlots]Value
}

// funcInfo is what a call needs that depends on the function alone,
// computed at its first call: its frame size and the values of its scalar
// constants, which are immutable and so shared by every activation (a
// constant then costs no allocation).
type funcInfo struct {
	frame  int
	consts []Value // by value ID; nil where the value is no scalar constant
	// calls are the direct calls' targets: an intrinsic's function or the
	// callee, looked up by name once rather than at every call
	calls map[*ir.Call]callTarget
	// code is the function's blocks compiled, by block ID
	// (specs/interp-compile)
	code []*cblock
}

// callTarget is what a direct call runs: an intrinsic or a function of the
// module (neither for a name the module does not define).
type callTarget struct {
	intrinsic intrinsicFn
	callee    *ir.Func
}

// info is f's funcInfo.
func (it *Interp) info(f *ir.Func) *funcInfo {
	if fi, ok := it.sh.funcs.Load(f); ok {
		return fi.(*funcInfo)
	}
	fi := &funcInfo{frame: frameSize(f), consts: make([]Value, f.NumValues)}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			switch c := in.(type) {
			case *ir.Const:
				if c.Bytes == nil && c.Dst.ID < len(fi.consts) {
					fi.consts[c.Dst.ID] = it.constValue(c)
				}
			case *ir.Call:
				if c.Fn != nil {
					continue
				}
				if fi.calls == nil {
					fi.calls = map[*ir.Call]callTarget{}
				}
				if c.Intrinsic != "" {
					fi.calls[c] = callTarget{intrinsic: intrinsics[c.Intrinsic]}
				} else {
					fi.calls[c] = callTarget{callee: it.mod.Func(c.Callee)}
				}
			}
		}
	}
	fi.code = compile(f, fi)
	it.sh.funcs.Store(f, fi)
	return fi
}

// at returns the position a trap at pos reports from this frame.
func (fr *frame) at(pos ast.Pos) ast.Pos {
	if fr.span != nil {
		return *fr.span
	}
	return pos
}

// spanAt is a stable pointer to the position a library call at *pos
// reports: the caller's own span inside the library, else the call site,
// whose position is fixed (pos points into the IR or a compiled op).
func (fr *frame) spanAt(pos *ast.Pos) *ast.Pos {
	if fr.span != nil {
		return fr.span
	}
	return pos
}

// noSpan is the span of a library activation entered without a call site.
var noSpan ast.Pos

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
	return it.callInto(f, args, span, nil, nil)
}

// callInto is callAt that, with into set, writes the return values into
// into's value slots ids and returns nil.
func (it *Interp) callInto(f *ir.Func, args []Value, span *ast.Pos, into *frame, ids []int) []Value {
	return it.callWith(f, nil, args, span, into, ids)
}

// callWith is callInto with the callee's funcInfo when the caller holds it
// (nil to look it up).
func (it *Interp) callWith(f *ir.Func, fi *funcInfo, args []Value, span *ast.Pos, into *frame, ids []int) []Value {
	if f.Intrinsic {
		name := intrinsicOf(f.Name)
		if fn, ok := intrinsics[name]; ok {
			return fn(it, args, callCtx{pos: f.Pos})
		}
		panic(fmt.Sprintf("interp: no intrinsic for %s", f.Name))
	}
	if fi == nil {
		fi = it.info(f)
	}
	frameBytes := fi.frame
	// a small activation's frame and slots come from one allocation
	// (specs/interp-call-path)
	n := len(f.Locals) + f.NumValues
	var fr *frame
	var slots []Value
	if n <= smallSlots {
		sf := &smallFrame{}
		fr, slots = &sf.fr, sf.slots[:n:n]
	} else {
		fr, slots = &frame{}, make([]Value, n)
	}
	fr.f, fr.file, fr.info = f, f.File, fi
	fr.retTo, fr.retIDs = into, ids
	prev := it.cur
	it.cur = fr
	if it.backtrace {
		it.frames = append(it.frames, fr)
	}
	defer func() {
		it.cur = prev
		if it.backtrace {
			it.frames = it.frames[:len(it.frames)-1]
		}
	}()
	if f.Library {
		// a library activation reports its caller's span and file; with no
		// call site (drop glue, a function value) an empty span and the
		// program's file, as natively
		fr.file = ""
		if span == nil {
			span = &noSpan
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
	fr.locals, fr.vals = slots[:len(f.Locals):len(f.Locals)], slots[len(f.Locals):]
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
	if !it.genericExec {
		return it.run(fr, fi.code, f.Entry)
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
				// a struct's fields are fixed once it is built, so the
				// slot is the field itself (no closure, no allocation)
				cur = slotRef{&cur.get().(*Struct).Fields[e.Field]}
			}
			seq = nil
		case ir.PathVariant:
			if rr, ok := cur.(rawRef); ok {
				cur = rr.variantField(e.Tag, e.Field)
			} else {
				// as a struct's: another variant is another *Union
				cur = slotRef{&cur.get().(*Union).Fields[e.Field]}
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
	// an array in raw memory (a field of a boxed or contained value) has
	// its length in its type: reading the place would decode every element
	if rr, ok := cur.(rawRef); ok {
		if arr, ok := rr.t.(*resolve.Array); ok {
			return arr.Len
		}
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
		if v := fr.info.consts[i.Dst.ID]; v != nil {
			fr.vals[i.Dst.ID] = v
		} else {
			fr.vals[i.Dst.ID] = it.constValue(i)
		}
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
		target := fr.info.calls[i]
		switch {
		case i.Intrinsic != "":
			if target.intrinsic == nil {
				panic(fmt.Sprintf("interp: unknown intrinsic %s", i.Intrinsic))
			}
			rets = target.intrinsic(it, args, callCtx{pos: fr.at(i.Pos), targs: i.TypeArgs})
		case i.Fn != nil:
			switch fv := fr.vals[i.Fn.ID].(type) {
			case ClosureVal:
				rets = it.callNamed(fv.Name, append(append([]Value(nil), fv.Env...), args...))
			default:
				rets = it.callNamed(fv.(FuncVal).Name, args)
			}
		default:
			switch callee := target.callee; {
			case callee != nil && callee.Library:
				// entering (or staying in) the library: traps report this call
				rets = it.callAt(callee, args, fr.spanAt(&i.Pos))
			case callee != nil:
				rets = it.callAt(callee, args, nil)
			default:
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
				fr.vals[i.Dst.ID] = intVal(v.Ty, -v.Bits)
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
		fr.vals[i.Dst.ID] = mkInt(tyUsize, uint64(it.length(fr, i.Place)))
	case *ir.MakeStruct:
		s := newStruct(len(i.Fields))
		for k, f := range i.Fields {
			s.Fields[k] = fr.vals[f.ID]
		}
		fr.vals[i.Dst.ID] = s
	case *ir.MakeUnion:
		u := newUnion(i.Tag, len(i.Fields))
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
		fr.vals[i.Dst.ID] = mkInt(tyU32, uint64(r.get().(*Union).Tag))
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
			elems[i] = mkInt(tyU8, uint64(b))
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
		return mkInt(tyOf(t.Name), v)
	}
	return Unit{}
}

// ---- arithmetic (spec §4.1) ----

// shiftCount is the shift count the operation uses: in wrapping mode the
// count masked to width - 1 (spec §4.1); otherwise the count as given, which
// the preceding check bounded below the width.
func shiftCount(n uint64, ty IntTy, mode string) uint64 {
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
				return intVal(ty, uint64(x+y))
			case "-":
				return intVal(ty, uint64(x-y))
			case "*":
				return intVal(ty, uint64(x*y))
			case "/":
				return intVal(ty, uint64(x/y))
			case "%":
				return intVal(ty, uint64(x%y))
			case "<<":
				return intVal(ty, uint64(x<<shiftCount(b.Bits, ty, mode)))
			case ">>":
				return intVal(ty, uint64(x>>shiftCount(b.Bits, ty, mode)))
			case "&":
				return intVal(ty, a.Bits&b.Bits)
			case "|":
				return intVal(ty, a.Bits|b.Bits)
			case "^":
				return intVal(ty, a.Bits^b.Bits)
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
			return intVal(ty, x+y)
		case "-":
			return intVal(ty, x-y)
		case "*":
			return intVal(ty, x*y)
		case "/":
			return intVal(ty, x/y)
		case "%":
			return intVal(ty, x%y)
		case "<<":
			return intVal(ty, x<<shiftCount(y, ty, mode))
		case ">>":
			return intVal(ty, x>>shiftCount(y, ty, mode))
		case "&":
			return intVal(ty, x&y)
		case "|":
			return intVal(ty, x|y)
		case "^":
			return intVal(ty, x^y)
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
	case *Array:
		// arrays compare element by element, as the native glue does
		b := r.(*Array)
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
		tt := tyOf(to)
		w := width(tt)
		if signed(tt) {
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
		tt := tyOf(to)
		w := width(tt)
		if signed(tt) {
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
		return intVal(tyOf(to), bitsv)
	case Float:
		if to == "f32" || to == "f64" {
			return mkFloat(to, v.V)
		}
		t := math.Trunc(v.V)
		tt := tyOf(to)
		w := width(tt)
		if saturate {
			if math.IsNaN(t) {
				return Int{Ty: tt}
			}
			if signed(tt) {
				if t < -math.Ldexp(1, int(w)-1) {
					return intVal(tt, uint64(minSigned(w)))
				}
				if t >= math.Ldexp(1, int(w)-1) {
					return intVal(tt, uint64(maxSigned(w)))
				}
			} else if t < 0 {
				return Int{Ty: tt}
			} else if t >= math.Ldexp(1, int(w)) {
				return intVal(tt, math.MaxUint64)
			}
		}
		if signed(tt) {
			return intVal(tt, uint64(int64(t)))
		}
		return intVal(tt, uint64(t))
	}
	return x
}

// ---- destruction (spec §5.7) ----

// destroy runs a value's destructor and destroys its fields in reverse
// declaration order. What it needs about the type (Copy or not, the
// destructor's name, the substituted field types) comes from the type's
// plan, computed once (specs/interp-destroy-plans).
func (it *Interp) destroy(v Value, t resolve.Type) {
	if v == nil {
		return
	}
	switch t.(type) {
	case *resolve.Array, *resolve.Named:
	default:
		return // no other type holds anything to destroy
	}
	p := it.planOf(t)
	if p.copy {
		return
	}
	if p.opaque {
		it.destroyLibrary(v, t.(*resolve.Named))
		return
	}
	if p.elem != nil {
		arr, ok := v.(*Array)
		if !ok {
			return
		}
		for i := len(arr.Elems) - 1; i >= 0; i-- {
			it.destroy(arr.Elems[i], p.elem)
		}
		return
	}
	if p.dtor != "" {
		slot := v
		it.callNamed(p.dtor, []Value{&Access{Ref: slotRef{&slot}, Edit: true}})
		v = slot
	}
	switch val := v.(type) {
	case *Struct:
		for i := len(p.fields) - 1; i >= 0; i-- {
			it.destroy(val.Fields[i], p.fields[i])
		}
	case *Union:
		fs := p.variants[val.Tag]
		for i := len(fs) - 1; i >= 0; i-- {
			it.destroy(val.Fields[i], fs[i])
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
		// under the lock that guards clone's increment: threads drop their
		// references concurrently (an unguarded decrement lost updates,
		// found by tests/concurrency/thread_detach_stress.kern)
		atomicMu.Lock()
		x.Count--
		last := x.Count == 0
		atomicMu.Unlock()
		if last {
			it.destroy(x.Val, elem(0))
		}
	case *Mutex:
		it.destroy(x.Val, elem(0))
	case *Condvar:
		// nothing is owned: waiters, if any, hold their own reference
	case *Scope:
		// the scope's end joins every thread it spawned, in creation
		// order, and destroys the result of each whose handle was
		// destroyed before; a live handle keeps its result
		for _, j := range x.Joins {
			it.await(j.done)
			j.mu.Lock()
			j.released = true
			left, ty := j.handle == joinDestroyed, j.elem
			j.mu.Unlock()
			if left && j.trap == nil {
				it.destroy(j.Result, ty)
			}
		}
	case *Thread:
		// A handle destroyed without join detaches the thread. A thread
		// that has finished has its result destroyed here; one still
		// running destroys its own result when its function returns.
		x.mu.Lock()
		finished := x.Done
		if !finished {
			x.detached, x.elem = true, elem(0)
		}
		x.mu.Unlock()
		if finished && x.trap == nil {
			it.destroy(x.Result, elem(0))
		}
	case *Join:
		// A handle destroyed while its scope is alive does not wait: it
		// leaves the result to the scope. After the scope the thread has
		// ended, and the result is the handle's to destroy.
		x.mu.Lock()
		after := x.released && x.handle == joinLive
		if x.handle == joinLive {
			x.handle, x.elem = joinDestroyed, elem(0)
		}
		x.mu.Unlock()
		if after && x.trap == nil {
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
	return mkInt(tyU32, uint64(n))
}
