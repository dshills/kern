package interp

import (
	"math"
	"os"
	"path/filepath"
	"syscall"
)

// std/io's files beyond read and write, metadata, paths and error kinds
// (specs/runtime RG1 to RG3), as the hosted runtime's rt_io.c does them:
// one system call each, returning its value or -errno. A descriptor is an
// index into the shared file table.

func init() {
	for k, v := range map[string]func(*Interp, []Value, callCtx) []Value{
		"io_seek":     ioSeek,
		"io_sync":     ioSync,
		"io_set_len":  ioSetLen,
		"io_stat":     ioStat,
		"io_fstat":    ioFstat,
		"io_rename":   func(_ *Interp, a []Value, _ callCtx) []Value { return twoPaths(a, syscall.Rename) },
		"io_link":     func(_ *Interp, a []Value, _ callCtx) []Value { return twoPaths(a, syscall.Link) },
		"io_symlink":  func(_ *Interp, a []Value, _ callCtx) []Value { return twoPaths(a, syscall.Symlink) },
		"io_chmod":    ioChmod,
		"io_readlink": ioReadlink,
		"io_realpath": ioRealpath,
		"io_getcwd":   ioGetcwd,
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
