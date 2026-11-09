package tests

import (
	"os"
	"syscall"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair: the test reads what a program
// writes to the terminal from the first file and gives the program the
// second as its standard output.
func openPTY() (master, slave *os.File, err error) {
	const (
		tiocptygrant = 0x20007454
		tiocptyunlk  = 0x20007452
		tiocptygname = 0x40807453
	)
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	ioctl := func(req uintptr, arg uintptr) error {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), req, arg); e != 0 {
			return e
		}
		return nil
	}
	var name [128]byte
	if err = ioctl(tiocptygrant, 0); err == nil {
		if err = ioctl(tiocptyunlk, 0); err == nil {
			err = ioctl(tiocptygname, uintptr(unsafe.Pointer(&name[0])))
		}
	}
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	slave, err = os.OpenFile(string(name[:n]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
