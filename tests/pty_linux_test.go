package tests

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair: the test reads what a program
// writes to the terminal from the first file and gives the program the
// second as its standard output.
func openPTY() (master, slave *os.File, err error) {
	const (
		tiocsptlck = 0x40045431
		tiocgptn   = 0x80045430
	)
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock, n int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), tiocsptlck, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		_ = master.Close()
		return nil, nil, e
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), tiocgptn, uintptr(unsafe.Pointer(&n))); e != 0 {
		_ = master.Close()
		return nil, nil, e
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
