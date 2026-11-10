package interp

import (
	"syscall"
	"time"
	"unsafe"
)

// The locks of an open file description (F_OFD_SETLK, F_OFD_SETLKW),
// which package syscall does not name.
const (
	fOFDSetlk  = 90
	fOFDSetlkw = 91
)

// soLinger is the linger option that counts in seconds: Darwin's
// SO_LINGER counts in ticks.
const soLinger = 0x1080

// setSigDefault gives a signal the system's default disposition in place
// of the Go runtime's handler. The system's own structure is the handler,
// a trampoline, a mask and flags.
func setSigDefault(sig syscall.Signal) error {
	var sa struct {
		handler, tramp uintptr
		mask           uint32
		flags          int32
	}
	if _, _, e := syscall.RawSyscall(syscall.SYS_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&sa)), 0); e != 0 {
		return e
	}
	return nil
}

// ---- the readiness queue (kqueue) ----

func pollerNew() (int, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return -1, err
	}
	syscall.CloseOnExec(kq)
	return kq, nil
}

// pollerSet watches fd for reading (interest 1), writing (2) or both, or
// stops watching it (0), as the runtime's kern_net_poller_set: one filter
// for each direction.
func pollerSet(kq, fd, interest int) error {
	found := false
	for f, filter := range [...]int16{syscall.EVFILT_READ, syscall.EVFILT_WRITE} {
		want := interest&(1<<f) != 0
		ch := []syscall.Kevent_t{{Ident: uint64(fd), Filter: filter, Flags: syscall.EV_DELETE}}
		if want {
			ch[0].Flags = syscall.EV_ADD
		}
		if _, err := syscall.Kevent(kq, ch, nil, nil); err != nil {
			if !want && err == syscall.ENOENT {
				continue
			}
			return err
		}
		found = true
	}
	if interest == 0 && !found {
		return syscall.ENOENT
	}
	return nil
}

// pollerWait waits once, at most d when timed, and returns the ready
// descriptors with the runtime's flags; a descriptor ready both ways is
// one entry.
func pollerWait(kq, max int, d time.Duration, timed bool) ([]readyFD, error) {
	evs := make([]syscall.Kevent_t, max)
	var ts *syscall.Timespec
	if timed {
		t := syscall.NsecToTimespec(int64(d))
		ts = &t
	}
	n, err := syscall.Kevent(kq, nil, evs, ts)
	if err != nil {
		return nil, err
	}
	var out []readyFD
	for _, ev := range evs[:n] {
		var fl uint64
		switch ev.Filter {
		case syscall.EVFILT_READ:
			fl = 1
		case syscall.EVFILT_WRITE:
			fl = 2
		}
		if ev.Flags&syscall.EV_EOF != 0 {
			fl |= 4
			if ev.Fflags != 0 {
				fl |= 8
			}
		}
		if ev.Flags&syscall.EV_ERROR != 0 {
			fl |= 8
		}
		k := 0
		for k < len(out) && out[k].fd != int(ev.Ident) {
			k++
		}
		if k == len(out) {
			out = append(out, readyFD{fd: int(ev.Ident)})
		}
		out[k].flags |= fl
	}
	return out, nil
}

// peerCred is the user, group and process of a Unix-domain stream's peer.
func peerCred(fd int) (uid, gid, pid uint32, err error) {
	var x struct {
		version uint32
		uid     uint32
		ngroups int16
		groups  [16]uint32
	}
	n := uint32(unsafe.Sizeof(x))
	// SOL_LOCAL is 0, LOCAL_PEERCRED 1 and LOCAL_PEERPID 2
	if _, _, e := syscall.Syscall6(syscall.SYS_GETSOCKOPT, uintptr(fd), 0, 1, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&n)), 0); e != 0 {
		return 0, 0, 0, e
	}
	if p, perr := syscall.GetsockoptInt(fd, 0, 2); perr == nil && p > 0 {
		pid = uint32(p)
	}
	return x.uid, x.groups[0], pid, nil
}

// This system has no abstract namespace for Unix-domain sockets.
func abstractName(string) (string, syscall.Errno) { return "", syscall.EOPNOTSUPP }
func fileName(path string) string                 { return path }
func senderPath(name string) []byte               { return []byte(name) }

// sockListening reports whether a socket is listening. This system
// defines the socket option that says so and refuses to read it; the
// socket's entry in the process's descriptor table has it
// (proc_pidfdinfo, flavor PROC_PIDFDSOCKETINFO: a socket_fdinfo of 792
// bytes whose soi_options are the 16 bits at offset 188).
func sockListening(fd int) bool {
	var info [792]byte
	n, _, e := syscall.Syscall6(syscall.SYS_PROC_INFO, 3, uintptr(syscall.Getpid()), 3, uintptr(fd), uintptr(unsafe.Pointer(&info[0])), uintptr(len(info)))
	if e != 0 || int(n) < len(info) {
		return false
	}
	options := uint16(info[188]) | uint16(info[189])<<8
	return options&syscall.SO_ACCEPTCONN != 0
}
