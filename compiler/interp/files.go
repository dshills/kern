package interp

import (
	"math"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// std/io's files beyond read and write, metadata, paths and error kinds
// (specs/runtime RG1 to RG3), as the hosted runtime's rt_io.c does them:
// one system call each, returning its value or -errno. A descriptor is an
// index into the shared file table.

func init() {
	for k, v := range map[string]func(*Interp, []Value, callCtx) []Value{
		"io_seek":       ioSeek,
		"io_sync":       ioSync,
		"io_set_len":    ioSetLen,
		"io_flock":      ioFlock,
		"io_lock_range": ioLockRange,
		"mem_map_file":  memMapFile,
		"mem_sync":      memSync,
		"io_stat":       ioStat,
		"io_fstat":      ioFstat,
		"io_rename":     func(_ *Interp, a []Value, _ callCtx) []Value { return twoPaths(a, syscall.Rename) },
		"io_link":       func(_ *Interp, a []Value, _ callCtx) []Value { return twoPaths(a, syscall.Link) },
		"io_symlink":    func(_ *Interp, a []Value, _ callCtx) []Value { return twoPaths(a, syscall.Symlink) },
		"io_chmod":      ioChmod,
		"io_readlink":   ioReadlink,
		"io_realpath":   ioRealpath,
		"io_getcwd":     ioGetcwd,
		"io_err_kind": func(_ *Interp, a []Value, _ callCtx) []Value {
			return i32Val(int32(errKind(syscall.Errno(int32(a[0].(Int).Bits)))))
		},
		"io_err_code": func(_ *Interp, a []Value, _ callCtx) []Value {
			return i32Val(int32(errCode(int(int32(a[0].(Int).Bits)))))
		},
	} {
		intrinsics[k] = v
	}
}

// openFlags turns std/io's OpenOptions bits (1 read, 2 write, 4 append, 8
// create, 16 truncate, 32 create_new) into the system's flags, as
// kern_io_open does.
func openFlags(opts uint32) int {
	flags := os.O_RDONLY
	if opts&(2|4) != 0 {
		flags = os.O_WRONLY
		if opts&1 != 0 {
			flags = os.O_RDWR
		}
	}
	if opts&4 != 0 {
		flags |= os.O_APPEND
	}
	if opts&32 != 0 {
		flags |= os.O_CREATE | os.O_EXCL
	} else {
		if opts&8 != 0 {
			flags |= os.O_CREATE
		}
		if opts&16 != 0 {
			flags |= os.O_TRUNC
		}
	}
	return flags
}

func ioSeek(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	whence := int(int32(a[2].(Int).Bits))
	if whence < 0 || whence > 2 {
		return i64Val(-int64(syscall.EINVAL))
	}
	n, err := f.Seek(signedOf(a[1].(Int)), whence)
	if err != nil {
		return i64Val(-int64(errnoOf(err)))
	}
	return i64Val(n)
}

func ioSync(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	if err := f.Sync(); err != nil { // F_FULLFSYNC on macOS, as the runtime asks
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

func ioSetLen(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	n := a[1].(Int).Bits
	if n > math.MaxInt64 {
		return i32Val(-int32(syscall.EINVAL))
	}
	if err := f.Truncate(int64(n)); err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

// statOut writes std/io's Metadata as the runtime's kern_stat_out does:
// the kind, the size, the modification time in nanoseconds since the Unix
// epoch (0 before it, the largest u64 when too late to represent) and the
// low 12 bits of the mode.
func statOut(st *syscall.Stat_t, dst Value) int32 {
	seq := byteSeq(acc(dst))
	if seq.length() < 4 {
		return -int32(syscall.EINVAL)
	}
	mode := uint32(st.Mode)
	kind := uint64(3)
	switch mode & syscall.S_IFMT {
	case syscall.S_IFREG:
		kind = 1
	case syscall.S_IFDIR:
		kind = 2
	case syscall.S_IFLNK:
		kind = 4
	}
	size := uint64(0)
	if st.Size > 0 {
		size = uint64(st.Size)
	}
	sec, nsec := statMtime(st)
	var mtime uint64
	switch {
	case sec < 0:
		mtime = 0
	case uint64(sec) > (math.MaxUint64-999999999)/1000000000:
		mtime = math.MaxUint64
	default:
		mtime = uint64(sec)*1000000000 + uint64(nsec)
	}
	for i, v := range []uint64{kind, size, mtime, uint64(mode & 0o7777)} {
		seq.at(i).set(mkInt(tyU64, v))
	}
	return 0
}

func ioStat(_ *Interp, a []Value, _ callCtx) []Value {
	p, e := cPath(seqBytes(acc(a[0])))
	if e != 0 {
		return i32Val(-int32(e))
	}
	var st syscall.Stat_t
	var err error
	if a[1].(Bool) {
		err = syscall.Stat(p, &st)
	} else {
		err = syscall.Lstat(p, &st)
	}
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(statOut(&st, a[2]))
}

func ioFstat(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	// through the file's own stat, which does not touch its descriptor's
	// blocking mode as Fd would
	fi, err := f.Stat()
	if err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return i32Val(-int32(syscall.EIO))
	}
	return i32Val(statOut(st, a[1]))
}

// twoPaths runs a system call that takes two paths: 0 or -errno.
func twoPaths(a []Value, call func(string, string) error) []Value {
	p, e := cPath(seqBytes(acc(a[0])))
	q := ""
	if e == 0 {
		q, e = cPath(seqBytes(acc(a[1])))
	}
	if e == 0 {
		if err := call(p, q); err != nil {
			e = errnoOf(err)
		}
	}
	return i32Val(-int32(e))
}

func ioChmod(_ *Interp, a []Value, _ callCtx) []Value {
	mode := uint32(a[1].(Int).Bits) & 0o7777
	return pathOp(a[0], func(p string) error { return syscall.Chmod(p, mode) })
}

// textOut copies text into the buffer when it fits and returns its
// length, as the runtime's kern_text_out.
func textOut(text string, dst Value) []Value {
	buf := byteSeq(acc(dst))
	if len(text) <= buf.length() {
		for i := 0; i < len(text); i++ {
			buf.at(i).set(mkInt(tyU8, uint64(text[i])))
		}
	}
	return i64Val(int64(len(text)))
}

func ioReadlink(_ *Interp, a []Value, _ callCtx) []Value {
	p, e := cPath(seqBytes(acc(a[0])))
	if e != 0 {
		return i64Val(-int64(e))
	}
	target := make([]byte, 4096)
	n, err := syscall.Readlink(p, target)
	if err != nil {
		return i64Val(-int64(errnoOf(err)))
	}
	return textOut(string(target[:n]), a[1])
}

// ioRealpath is realpath(3): the absolute path with every symbolic link,
// `.` and `..` resolved, of a path that exists.
func ioRealpath(_ *Interp, a []Value, _ callCtx) []Value {
	p, e := cPath(seqBytes(acc(a[0])))
	if e != 0 {
		return i64Val(-int64(e))
	}
	if p == "" {
		return i64Val(-int64(syscall.ENOENT))
	}
	if !filepath.IsAbs(p) {
		wd, err := syscall.Getwd()
		if err != nil {
			return i64Val(-int64(errnoOf(err)))
		}
		p = wd + "/" + p
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return i64Val(-int64(errnoOf(err)))
	}
	return textOut(real, a[1])
}

func ioGetcwd(_ *Interp, a []Value, _ callCtx) []Value {
	// the system's answer, not $PWD's: os.Getwd would return a path
	// through a symbolic link when the environment names one
	wd, err := syscall.Getwd()
	if err != nil {
		return i64Val(-int64(errnoOf(err)))
	}
	return textOut(wd, a[0])
}

// errKinds lists, per ordinal of std/io's ErrKind, the system errors of
// that kind by name; the first is the one that names the kind
// (kern_io_err_code). Every other error is Other (19).
var errKinds = [][]syscall.Errno{
	{syscall.ENOENT},
	{syscall.EACCES, syscall.EPERM},
	{syscall.EEXIST},
	{syscall.EINVAL, syscall.ENAMETOOLONG},
	{syscall.EISDIR},
	{syscall.ENOTDIR},
	{syscall.ENOTEMPTY},
	{syscall.EPIPE},
	{syscall.ECONNREFUSED},
	{syscall.ECONNRESET},
	{syscall.ECONNABORTED},
	{syscall.ENOTCONN},
	{syscall.EADDRINUSE},
	{syscall.EADDRNOTAVAIL},
	{syscall.ETIMEDOUT},
	{syscall.EINTR},
	{syscall.EAGAIN, syscall.EWOULDBLOCK},
	{syscall.ENOMEM},
	{syscall.ENOTSUP, syscall.ENOSYS, syscall.EOPNOTSUPP},
}

func errKind(code syscall.Errno) int {
	for k, names := range errKinds {
		for _, n := range names {
			if n == code {
				return k
			}
		}
	}
	return len(errKinds)
}

func errCode(kind int) int {
	if kind >= 0 && kind < len(errKinds) {
		return int(errKinds[kind][0])
	}
	return int(syscall.EIO)
}

// ioFlock takes or gives up an advisory lock, as the runtime's
// kern_io_flock: op 0 shared, 1 exclusive, 2 unlock, 4 added not to wait.
func ioFlock(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	op := int32(a[1].(Int).Bits)
	if op < 0 || op > 5 || op&3 == 3 {
		return i32Val(-int32(syscall.EINVAL))
	}
	how := [...]int{syscall.LOCK_SH, syscall.LOCK_EX, syscall.LOCK_UN}[op&3]
	if op&4 != 0 {
		how |= syscall.LOCK_NB
	}
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err == nil {
			return i32Val(0)
		}
		if err != syscall.EINTR {
			return i32Val(-int32(errnoOf(err)))
		}
	}
}

// ioLockRange takes or gives up a lock on a range of a file's bytes, as
// the runtime's kern_io_lock_range: the lock of the open file
// description, op as ioFlock's, a length of 0 for every byte from start.
func ioLockRange(it *Interp, a []Value, _ callCtx) []Value {
	f := it.openFile(signedOf(a[0].(Int)))
	if f == nil {
		return i32Val(-int32(syscall.EBADF))
	}
	op := int32(a[1].(Int).Bits)
	start, n := u64(a[2]), u64(a[3])
	if op < 0 || op > 5 || op&3 == 3 || start > math.MaxInt64 || n > math.MaxInt64 {
		return i32Val(-int32(syscall.EINVAL))
	}
	lk := syscall.Flock_t{Type: [...]int16{syscall.F_RDLCK, syscall.F_WRLCK, syscall.F_UNLCK}[op&3], Start: int64(start), Len: int64(n)}
	cmd := fOFDSetlkw
	if op&4 != 0 || op&3 == 2 {
		cmd = fOFDSetlk
	}
	for {
		err := syscall.FcntlFlock(f.Fd(), cmd, &lk)
		if err == nil {
			return i32Val(0)
		}
		if err == syscall.EACCES {
			return i32Val(-int32(syscall.EAGAIN)) // a conflict is one error, whichever the system names it
		}
		if err == syscall.EBADF {
			return i32Val(-int32(syscall.EINVAL)) // the file is not open the way the lock needs
		}
		if err != syscall.EINTR {
			return i32Val(-int32(errnoOf(err)))
		}
	}
}

// fileMap is a file mapping (std/mem, specs/runtime RL4, RM5): memory the
// system mapped from the file, shared, as the runtime's is, so that a
// write through it is a write to the file and another program's writes
// are seen. The interpreter gives it an address of its own.
type fileMap struct {
	mem []byte
}

func memMapFile(it *Interp, a []Value, _ callCtx) []Value {
	fd := signedOf(a[0].(Int))
	n, writable := u64(a[1]), bool(a[2].(Bool))
	it.sh.mu.Lock()
	f := it.sh.files[fd]
	it.sh.mu.Unlock()
	if f == nil {
		return i64Val(-int64(syscall.EBADF))
	}
	if n == 0 || n > math.MaxInt {
		return i64Val(-int64(syscall.EINVAL))
	}
	prot := syscall.PROT_READ
	if writable {
		prot |= syscall.PROT_WRITE
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, int(n), prot, syscall.MAP_SHARED)
	if err != nil {
		return i64Val(-int64(errnoOf(err)))
	}
	addr := it.heap.adopt(mem, !writable, uint64(os.Getpagesize()))
	it.sh.mu.Lock()
	if it.sh.maps == nil {
		it.sh.maps = map[uint64]*fileMap{}
	}
	it.sh.maps[addr] = &fileMap{mem: mem}
	it.sh.mu.Unlock()
	return i64Val(int64(addr))
}

// memSync returns when the system has written a mapping's changed pages
// to its file.
func memSync(it *Interp, a []Value, _ callCtx) []Value {
	addr := u64(a[0])
	it.sh.mu.Lock()
	m := it.sh.maps[addr]
	it.sh.mu.Unlock()
	if m == nil || len(m.mem) == 0 {
		return i32Val(0)
	}
	if _, _, e := syscall.Syscall(syscall.SYS_MSYNC, uintptr(unsafe.Pointer(&m.mem[0])), uintptr(len(m.mem)), syscall.MS_SYNC); e != 0 {
		return i32Val(-int32(e))
	}
	return i32Val(0)
}

// unmapFile ends a file mapping at addr, if there is one: the memory
// leaves the interpreter's address space first, so that an access after
// this is a trap, and is then unmapped. The block's record is freed by
// mem_unmap.
func (it *Interp) unmapFile(addr uint64) {
	it.sh.mu.Lock()
	m := it.sh.maps[addr]
	delete(it.sh.maps, addr)
	it.sh.mu.Unlock()
	if m != nil {
		if mem := it.heap.release(addr); mem != nil {
			_ = syscall.Munmap(mem)
		}
	}
}
