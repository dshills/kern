package interp

import (
	"syscall"
	"time"
	"unsafe"
)

// The locks of an open file description (F_OFD_SETLK, F_OFD_SETLKW),
// which package syscall does not name.
const (
	fOFDSetlk  = 37
	fOFDSetlkw = 38
)

const soLinger = syscall.SO_LINGER

// setSigDefault gives a signal the system's default disposition in place
// of the Go runtime's handler. The kernel's structure is the handler,
// flags, a restorer and a mask of 8 bytes.
func setSigDefault(sig syscall.Signal) error {
	var sa struct {
		handler  uintptr
		flags    uint64
		restorer uintptr
		mask     uint64
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&sa)), 0, 8, 0, 0); e != 0 {
		return e
	}
	return nil
}

// ---- the readiness queue (epoll) ----

func pollerNew() (int, error) { return syscall.EpollCreate1(syscall.EPOLL_CLOEXEC) }

// pollerSet watches fd for reading (interest 1), writing (2) or both, or
// stops watching it (0), as the runtime's kern_net_poller_set.
func pollerSet(ep, fd, interest int) error {
	if interest == 0 {
		return syscall.EpollCtl(ep, syscall.EPOLL_CTL_DEL, fd, nil)
	}
	ev := syscall.EpollEvent{Fd: int32(fd)}
	if interest&1 != 0 {
		ev.Events |= syscall.EPOLLIN | syscall.EPOLLRDHUP
	}
	if interest&2 != 0 {
		ev.Events |= syscall.EPOLLOUT
	}
	err := syscall.EpollCtl(ep, syscall.EPOLL_CTL_MOD, fd, &ev)
	if err == syscall.ENOENT {
		err = syscall.EpollCtl(ep, syscall.EPOLL_CTL_ADD, fd, &ev)
	}
	return err
}

// pollerWait waits once, at most d when timed (in whole milliseconds,
// rounded up), and returns the ready descriptors with the runtime's
// flags.
func pollerWait(ep, max int, d time.Duration, timed bool) ([]readyFD, error) {
	evs := make([]syscall.EpollEvent, max)
	ms := -1
	if timed {
		ms = int(min((d+time.Millisecond-1)/time.Millisecond, 1<<30))
	}
	n, err := syscall.EpollWait(ep, evs, ms)
	if err != nil {
		return nil, err
	}
	out := make([]readyFD, 0, n)
	for _, ev := range evs[:n] {
		var fl uint64
		if ev.Events&syscall.EPOLLIN != 0 {
			fl |= 1
		}
		if ev.Events&syscall.EPOLLOUT != 0 {
			fl |= 2
		}
		if ev.Events&(syscall.EPOLLRDHUP|syscall.EPOLLHUP) != 0 {
			fl |= 4
		}
		if ev.Events&syscall.EPOLLERR != 0 {
			fl |= 8
		}
		out = append(out, readyFD{fd: int(ev.Fd), flags: fl})
	}
	return out, nil
}

// peerCred is the user, group and process of a Unix-domain stream's peer.
func peerCred(fd int) (uid, gid, pid uint32, err error) {
	c, err := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		return 0, 0, 0, err
	}
	if c.Pid > 0 {
		pid = uint32(c.Pid)
	}
	return c.Uid, c.Gid, pid, nil
}

// A name in the abstract namespace is, to the network package the
// interpreter stands on, a name that begins with `@`; and so is a file's
// relative path that begins with `@`, which is therefore given as
// `./@...`, the same file. A sender's path that begins with `@` is read
// as an abstract name.
func abstractName(rest string) (string, syscall.Errno) { return "@" + rest, 0 }
func fileName(path string) string {
	if path[0] == '@' {
		return "./" + path
	}
	return path
}
func senderPath(name string) []byte {
	if len(name) > 0 && name[0] == '@' {
		return append([]byte{0}, name[1:]...)
	}
	return []byte(name)
}

// sockListening reports whether a socket is listening.
func sockListening(fd int) bool {
	v, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	return err == nil && v != 0
}
