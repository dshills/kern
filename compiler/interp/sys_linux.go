package interp

import (
	"syscall"
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
