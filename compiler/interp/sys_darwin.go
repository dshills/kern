package interp

import (
	"syscall"
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
