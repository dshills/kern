package interp

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// std/process's runtime entries (specs/std-process, PLAN.md "Interpreter
// spawn"). Children are real processes started with syscall.ForkExec, so
// that a pid is waited with syscall.Wait4 and WNOHANG works. A handle is a
// key of sharedState.procs.
//
// Descriptors: Go creates its own descriptors close-on-exec and ForkExec
// passes only the three standard ones, but it closes nothing else, so
// every spawn first marks each descriptor from 3 up close-on-exec (the
// sweep). This changes the host process's flags: its own later children
// do not inherit those descriptors either. A descriptor an embedder
// creates without the flag on another thread while a spawn runs is outside
// the kern program and not covered.

// procEntry is one started child.
type procEntry struct {
	pid       int
	fwd       []*forwarder // inherited streams copied into captured writers
	flushOnce sync.Once
}

// spawnMu serialises the sweep and the spawn across every interpreter in
// the process.
var spawnMu sync.Mutex

// procHook, when set by a test, is called by a forwarder on each state
// change and before each blocking read, around a flush request, and at
// the start of each wait.
var procHook func(state string)

func init() {
	for k, v := range map[string]intrinsicFn{
		"proc_spawn":           procSpawn,
		"proc_pid":             procPid,
		"proc_wait":            procWait,
		"proc_signal":          procSignal,
		"proc_release":         procRelease,
		"proc_flush_inherited": procFlushInherited,
		"proc_close":           procClose,
		"proc_read":            procRead,
		"proc_write":           procWrite,
		"proc_errno":           procErrno,
		"proc_fault_arm":       procFaultArm,
		"proc_fault_take":      procFaultTake,
	} {
		intrinsics[k] = v
	}
}

func i64Val(v int64) []Value { return []Value{mkInt(tyI64, uint64(v))} }
func i32Val(v int32) []Value { return []Value{mkInt(tyI32, uint64(uint32(v)))} }

// unpack splits count NUL-terminated strings packed back to back, or
// reports false when buf is not exactly that.
func unpack(buf []byte, count int64) ([]string, bool) {
	if count < 0 || int64(bytes.Count(buf, []byte{0})) != count || (len(buf) > 0 && buf[len(buf)-1] != 0) {
		return nil, false
	}
	out := make([]string, 0, count)
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, 0)
		out = append(out, string(buf[:i]))
		buf = buf[i+1:]
	}
	return out, true
}

// fdOpen reports whether descriptor fd is open.
func fdOpen(fd int) bool {
	var st syscall.Stat_t
	return syscall.Fstat(fd, &st) == nil
}

// rawFd is f's descriptor. Unlike f.Fd it leaves the descriptor's mode
// alone: Fd switches it to blocking, after which read deadlines, which
// the forwarders rely on, no longer work.
func rawFd(f *os.File) int {
	fd := -1
	if rc, err := f.SyscallConn(); err == nil {
		_ = rc.Control(func(u uintptr) { fd = int(u) })
	}
	return fd
}

// above2 returns f, or a duplicate of it numbered 3 or more when f's
// descriptor is below 3, so that no descriptor std/process creates takes
// the place of a closed standard one. The duplicate keeps f's mode.
func above2(f *os.File) (*os.File, error) {
	fd := rawFd(f)
	if fd > 2 {
		return f, nil
	}
	syscall.ForkLock.RLock() // the duplicates exist briefly without close-on-exec
	defer syscall.ForkLock.RUnlock()
	var low []int
	defer func() {
		for _, d := range low {
			_ = syscall.Close(d)
		}
	}()
	for {
		d, err := syscall.Dup(fd)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if d > 2 {
			syscall.CloseOnExec(d)
			_ = f.Close()
			return os.NewFile(uintptr(d), f.Name()), nil
		}
		low = append(low, d)
	}
}

// sweep marks every descriptor from 3 up close-on-exec. It fails closed:
// a descriptor list that cannot be read stops the spawn.
func sweep() error {
	d, err := os.Open("/dev/fd")
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(-1)
	_ = d.Close()
	if err != nil {
		return err
	}
	for _, n := range names {
		if fd, err := strconv.Atoi(n); err == nil && fd > 2 {
			syscall.CloseOnExec(fd) // one closed meanwhile (the listing's own) cannot leak
		}
	}
	return nil
}

// executable reports whether path names a regular file the process may
// execute (symbolic links followed).
func executable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && syscall.Access(path, 1) == nil
}

// spawnErr is a spawn's -errno result.
func spawnErr(err error) []Value { return i64Val(-int64(errnoOf(err))) }

// procSpawn starts a child (R4), in R4's order: (s) the streams, (a)
// entering dir, (b) finding the program (R2), (c) executing it.
func procSpawn(it *Interp, a []Value, _ callCtx) []Value {
	prog, argvb, argc := seqBytes(acc(a[0])), seqBytes(acc(a[1])), int64(u64(a[2]))
	envb, envc, dir := seqBytes(acc(a[3])), int64(u64(a[4])), seqBytes(acc(a[5]))
	hasDir, search, modes := bool(a[6].(Bool)), seqBytes(acc(a[7])), seqBytes(acc(a[8]))
	fds := byteSeq(acc(a[9]))
	einval := i64Val(-int64(syscall.EINVAL))
	if len(prog) == 0 || len(prog) >= 4096 || len(dir) >= 4096 || len(modes) != 3 || fds.length() < 3 || argc < 1 {
		return einval
	}
	for _, m := range modes {
		if m > 2 {
			return einval
		}
	}
	argv, ok := unpack(argvb, argc)
	if !ok {
		return einval
	}
	envv, ok := unpack(envb, envc)
	if !ok {
		return einval
	}
	for i := 0; i < 3; i++ {
		fds.at(i).set(mkInt(tyI32, uint64(uint32(0xffffffff))))
	}
	spawnMu.Lock()
	defer spawnMu.Unlock()
	if err := sweep(); err != nil {
		return spawnErr(err)
	}
	// (s) the streams
	var parentEnds, childEnds []*os.File
	defer func() {
		for _, f := range childEnds {
			_ = f.Close()
		}
	}()
	fail := func(err error) []Value {
		for _, f := range parentEnds {
			_ = f.Close()
		}
		return spawnErr(err)
	}
	files := make([]uintptr, 3)
	var fwd []*forwarder
	var piped [3]*os.File // the parent's pipe ends, handed to the Child
	for i, m := range modes {
		switch {
		case m == 2:
			r, w, err := os.Pipe()
			if err != nil {
				return fail(err)
			}
			if r, err = above2(r); err != nil {
				_ = w.Close()
				return fail(err)
			}
			if w, err = above2(w); err != nil {
				_ = r.Close()
				return fail(err)
			}
			child, parent := w, r
			if i == 0 {
				child, parent = r, w
			}
			parentEnds = append(parentEnds, parent)
			childEnds = append(childEnds, child)
			files[i] = child.Fd() // blocking, as a child expects; the parent's end keeps Go's mode
			piped[i] = parent
		case m == 1:
			flag := os.O_WRONLY
			if i == 0 {
				flag = os.O_RDONLY
			}
			f, err := os.OpenFile(os.DevNull, flag, 0)
			if err == nil {
				f, err = above2(f)
			}
			if err != nil {
				return fail(err)
			}
			childEnds = append(childEnds, f)
			files[i] = f.Fd()
		case i == 0:
			files[i] = inheritFd(0)
		default:
			w := it.out
			if i == 2 {
				w = it.errOut
			}
			// a file the child can share as is: its own descriptor. A
			// non-blocking one (a pollable pipe of the host's) is copied
			// through a pipe instead, since the child would share its mode
			// and see EAGAIN; f.Fd would switch the host's file to blocking.
			if lw, ok := w.(*lockedWriter); ok {
				if f, ok := lw.w.(*os.File); ok {
					if fd := rawFd(f); fd >= 0 && !nonblocking(fd) {
						files[i] = inheritFd(fd)
						continue
					}
				}
			}
			r, wr, err := os.Pipe()
			if err != nil {
				return fail(err)
			}
			// above2 closes the file it is given when it fails, so only
			// the other end is left to close
			if r, err = above2(r); err != nil {
				_ = wr.Close()
				return fail(err)
			}
			if wr, err = above2(wr); err != nil {
				_ = r.Close()
				return fail(err)
			}
			parentEnds = append(parentEnds, r)
			childEnds = append(childEnds, wr)
			files[i] = wr.Fd()
			fwd = append(fwd, newForwarder(r, w))
		}
	}
	// (a) the working directory, (b) the program, both absolute when a
	// relative path is involved, so that nothing is resolved twice
	sd, sp := string(dir), string(prog)
	absD, absP, slash := strings.HasPrefix(sd, "/"), strings.HasPrefix(sp, "/"), strings.Contains(sp, "/")
	var cwd string
	if (hasDir && !absD) || (slash && !absP && (!hasDir || !absD)) {
		wd, err := syscall.Getwd()
		if err != nil {
			return fail(err)
		}
		cwd = wd
	}
	absDir := ""
	if hasDir {
		fi, err := os.Stat(sd)
		if err != nil {
			return fail(err)
		}
		if !fi.IsDir() {
			return fail(syscall.ENOTDIR)
		}
		if err := syscall.Access(sd, 1); err != nil {
			return fail(err)
		}
		absDir = sd
		if !absD {
			absDir = cwd + "/" + sd
		}
	}
	var path string
	switch {
	case absP:
		path = sp
	case slash:
		base := cwd
		if hasDir {
			base = absDir
		}
		path = base + "/" + sp
	default:
		for _, d := range bytes.Split(search, []byte{':'}) {
			if len(d) == 0 || d[0] != '/' { // empty and relative elements are skipped (R2)
				continue
			}
			if c := string(d) + "/" + sp; executable(c) {
				path = c
				break
			}
		}
		if path == "" {
			return fail(syscall.ENOENT)
		}
	}
	if len(path) >= 4096 || len(absDir) >= 4096 {
		return fail(syscall.ENAMETOOLONG)
	}
	// (c) the child
	pid, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{Dir: absDir, Env: envv, Files: files})
	if err != nil {
		return fail(err)
	}
	for _, f := range fwd {
		go f.run()
	}
	for i, f := range piped {
		if f != nil {
			fds.at(i).set(mkInt(tyI32, uint64(uint32(int32(it.addFile(f))))))
		}
	}
	it.sh.mu.Lock()
	if it.sh.procs == nil {
		it.sh.procs = map[int64]*procEntry{}
	}
	it.sh.nextProc++
	h := it.sh.nextProc
	it.sh.procs[h] = &procEntry{pid: pid, fwd: fwd}
	it.sh.mu.Unlock()
	return i64Val(h)
}

// nonblocking reports whether descriptor fd is in non-blocking mode.
func nonblocking(fd int) bool {
	fl, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
	return e == 0 && fl&syscall.O_NONBLOCK != 0
}

// inheritFd is descriptor fd for a child's inherited stream, or closed in
// the child when it is closed here.
func inheritFd(fd int) uintptr {
	if fdOpen(fd) {
		return uintptr(fd)
	}
	return ^uintptr(0)
}

func (it *Interp) proc(h int64) *procEntry {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	return it.sh.procs[h]
}

func procPid(it *Interp, a []Value, _ callCtx) []Value {
	if p := it.proc(signedOf(a[0].(Int))); p != nil {
		return []Value{mkInt(tyU32, uint64(p.pid))}
	}
	return []Value{mkInt(tyU32, 0)}
}

// procWait waits for the child (block) or checks whether it has ended: 1
// with the status in st, 0 while it runs, or -errno. It only reaps;
// forwarding is completed by procFlushInherited.
func procWait(it *Interp, a []Value, _ callCtx) []Value {
	if procHook != nil {
		procHook("wait")
	}
	p := it.proc(signedOf(a[0].(Int)))
	if p == nil {
		return i32Val(-int32(syscall.ESRCH))
	}
	st := byteSeq(acc(a[2]))
	if st.length() < 2 {
		return i32Val(-int32(syscall.EINVAL))
	}
	opts := 0
	if !bool(a[1].(Bool)) {
		opts = syscall.WNOHANG
	}
	var ws syscall.WaitStatus
	var r int
	var err error
	for {
		r, err = syscall.Wait4(p.pid, &ws, opts, nil)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	if r == 0 {
		return i32Val(0)
	}
	kind, val := 0, ws.ExitStatus()
	if ws.Signaled() {
		kind, val = 1, int(ws.Signal())
	}
	st.at(0).set(mkInt(tyI32, uint64(uint32(int32(kind)))))
	st.at(1).set(mkInt(tyI32, uint64(uint32(int32(val)))))
	return i32Val(1)
}

func procSignal(it *Interp, a []Value, _ callCtx) []Value {
	p := it.proc(signedOf(a[0].(Int)))
	if p == nil {
		return i32Val(-int32(syscall.ESRCH))
	}
	for {
		err := syscall.Kill(p.pid, syscall.Signal(signedOf(a[1].(Int))))
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return i32Val(-int32(errnoOf(err)))
		}
		return i32Val(0)
	}
}

func procRelease(it *Interp, a []Value, _ callCtx) []Value {
	it.sh.mu.Lock()
	delete(it.sh.procs, signedOf(a[0].(Int)))
	it.sh.mu.Unlock()
	return nil
}

// procFlushInherited is the completion barrier of inherited streams that
// are copied into captured writers: when it returns, everything the
// reaped child wrote to them is in the writers. It runs once per handle.
func procFlushInherited(it *Interp, a []Value, _ callCtx) []Value {
	if p := it.proc(signedOf(a[0].(Int))); p != nil {
		p.flushOnce.Do(func() {
			for _, f := range p.fwd {
				f.flushNow()
			}
		})
	}
	return nil
}

// addFile enters f in the shared file table (std/io's descriptors, from 3
// up) and returns its key.
func (it *Interp) addFile(f *os.File) int64 {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	if it.sh.files == nil {
		it.sh.files = map[int64]*os.File{}
	}
	fd := int64(3)
	for it.sh.files[fd] != nil {
		fd++
	}
	it.sh.files[fd] = f
	return fd
}

func (it *Interp) openFile(fd int64) *os.File {
	it.sh.mu.Lock()
	defer it.sh.mu.Unlock()
	return it.sh.files[fd]
}

// procRead reads from a pipe end: bytes read, 0 at end of file, or -errno.
func procRead(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	seq := byteSeq(acc(a[1]))
	if seq.length() == 0 {
		return i64Val(0)
	}
	buf := make([]byte, seq.length())
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		if errors.Is(err, io.EOF) {
			return i64Val(0)
		}
		return i64Val(-int64(errnoOf(err)))
	}
	for i := 0; i < n; i++ {
		seq.at(i).set(mkInt(tyU8, uint64(buf[i])))
	}
	return i64Val(int64(n))
}

// procWrite writes to a pipe end: bytes written or -errno. Go reports a
// write to a closed pipe on a descriptor other than 1 and 2 as EPIPE and
// does not end the process (R6).
func procWrite(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	n, err := f.Write(seqBytes(acc(a[1])))
	if err != nil && n == 0 {
		return i64Val(-int64(errnoOf(err)))
	}
	return i64Val(int64(n))
}

func procClose(it *Interp, a []Value, _ callCtx) []Value {
	fd := signedOf(a[0].(Int))
	it.sh.mu.Lock()
	f := it.sh.files[fd]
	delete(it.sh.files, fd)
	it.sh.mu.Unlock()
	if f == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	if err := f.Close(); err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

func procErrno(it *Interp, a []Value, _ callCtx) []Value {
	codes := map[uint64]syscall.Errno{0: syscall.EINVAL, 1: syscall.ENAMETOOLONG, 2: syscall.EBADF, 3: syscall.EPIPE, 5: syscall.EAGAIN}
	if c, ok := codes[u64(a[0])]; ok {
		return i32Val(int32(c))
	}
	return i32Val(int32(syscall.EIO))
}

// procFaultArm arms the fault hook's slot for a pipe kind; it exists only
// under --test-hooks, as the native runtime's does.
func procFaultArm(it *Interp, a []Value, c callCtx) []Value {
	if !it.TestHooks {
		it.trap("explicit", c.pos)
	}
	if k := u64(a[0]); k <= 2 {
		it.sh.procFaults[k].Store(int32(signedOf(a[1].(Int))))
	}
	return nil
}

// procFaultTake takes and clears the slot for a pipe kind: 0 when unarmed.
func procFaultTake(it *Interp, a []Value, _ callCtx) []Value {
	k := u64(a[0])
	if k > 2 || it.sh.procFaults[k].Load() == 0 {
		return i32Val(0)
	}
	return i32Val(it.sh.procFaults[k].Swap(0))
}

// forwarder copies a child's inherited stream into a captured writer. It
// is the pipe's only reader and owns the read end. Its states, each change
// made under mu: forwarding (reading and writing chunk by chunk), flushing
// (after a flush request: draining exactly the bytes unread at that
// moment) and done (end of file or a read error).
type forwarder struct {
	r       *os.File
	w       io.Writer
	mu      sync.Mutex
	flush   bool // a flush was requested
	flushed bool // and is complete
	discard bool // the writer failed: keep reading, write nothing
	flushCh chan struct{}
	doneCh  chan struct{}
}

func newForwarder(r *os.File, w io.Writer) *forwarder {
	return &forwarder{r: r, w: w, flushCh: make(chan struct{}), doneCh: make(chan struct{})}
}

func (f *forwarder) hook(state string) {
	if procHook != nil {
		procHook(state)
	}
}

func (f *forwarder) write(b []byte) {
	if f.discard {
		return
	}
	if _, err := f.w.Write(b); err != nil {
		f.discard = true // R4: the child must never block on a failed writer
	}
}

func (f *forwarder) run() {
	buf := make([]byte, 32<<10)
	for {
		f.hook("check")
		f.mu.Lock()
		if f.flush && !f.flushed {
			// the deadline was set before the request was published, under
			// this lock; clear it so that the raw drain is not refused
			_ = f.r.SetReadDeadline(time.Time{})
			f.mu.Unlock()
			f.hook("flushing")
			if f.drain(buf) {
				f.finish()
				return
			}
			f.mu.Lock()
			f.flushed = true
			close(f.flushCh)
			f.mu.Unlock()
			f.hook("flushed")
			continue
		}
		f.mu.Unlock()
		f.hook("read")
		n, err := f.r.Read(buf)
		if n > 0 {
			f.write(buf[:n])
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue // only means "check for a request"
			}
			f.finish()
			return
		}
	}
}

// drain reads and writes exactly the bytes unread now, and reports whether
// it reached end of file or a read error (the done state).
func (f *forwarder) drain(buf []byte) bool {
	rc, err := f.r.SyscallConn()
	if err != nil {
		return false
	}
	var n int32
	var e syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(fionread), uintptr(unsafe.Pointer(&n)))
	}); err != nil || e != 0 {
		return false
	}
	left := int(n)
	for left > 0 {
		chunk := buf
		if left < len(chunk) {
			chunk = chunk[:left]
		}
		var got int
		var rerr error
		if err := rc.Read(func(fd uintptr) bool {
			for {
				got, rerr = syscall.Read(int(fd), chunk)
				if !errors.Is(rerr, syscall.EINTR) {
					return true // never wait in the poller
				}
			}
		}); err != nil {
			return true
		}
		switch {
		case errors.Is(rerr, syscall.EAGAIN):
			return false // nothing more now (cannot happen while this is the only reader)
		case rerr != nil, got == 0:
			return true
		}
		f.write(chunk[:got])
		left -= got
	}
	return false
}

func (f *forwarder) finish() {
	_ = f.r.Close()
	f.mu.Lock()
	if f.flush && !f.flushed {
		f.flushed = true
		close(f.flushCh)
	}
	f.mu.Unlock()
	close(f.doneCh)
	f.hook("done")
}

// flushNow requests a flush and waits until it is complete or the
// forwarder is done.
func (f *forwarder) flushNow() {
	f.hook("request")
	f.mu.Lock()
	f.flush = true
	_ = f.r.SetReadDeadline(time.Now()) // a blocked Read returns without consuming data
	f.mu.Unlock()
	f.hook("requested")
	select {
	case <-f.flushCh:
	case <-f.doneCh:
	}
}
