package interp

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// timedOut is io_read_timeout's result when nothing arrived in time:
// -2^32, outside every errno value (specs/std-timeouts).
const timedOut = -int64(1) << 32

// stdinPump reads standard input on one goroutine once a timed read has
// asked for it (specs/std-timeouts): a blocked os.Stdin.Read cannot be
// abandoned, so a timed-out read leaves the pending read to the pump, and
// every later read of descriptor 0, timed or not, takes its bytes from
// the pump. No byte is lost or read twice. It reads ahead at most
// pumpLimit bytes, each read sized to the room left: when full it waits
// for the program to read, so a writer that never stops cannot grow it
// without bound.
type stdinPump struct {
	mu      sync.Mutex
	data    []byte
	err     error         // the read error that ended the pump (io.EOF at end of file)
	ready   chan struct{} // a token whenever data or err changes
	drained *sync.Cond    // signalled when a read takes bytes
}

// pumpLimit is the pump's read-ahead before it waits for the program.
const pumpLimit = 64 << 10

func newStdinPump() *stdinPump {
	p := &stdinPump{ready: make(chan struct{}, 1)}
	p.drained = sync.NewCond(&p.mu)
	return p
}

// The pump is per process, as os.Stdin is: a second interpreter in the
// same process (kern test, an in-process host) shares it, so no reader
// blocked for an earlier program takes bytes meant for a later one.
var (
	pumpOnce sync.Once
	pumpPtr  atomic.Pointer[stdinPump]
)

// stdinPump starts the process's pump on first use and returns it.
func (it *Interp) stdinPump() *stdinPump {
	pumpOnce.Do(func() {
		p := newStdinPump()
		pumpPtr.Store(p)
		go p.run(os.Stdin)
	})
	return pumpPtr.Load()
}

// pumpIfStarted is the pump when a timed read started one, else nil.
func (it *Interp) pumpIfStarted() *stdinPump { return pumpPtr.Load() }

// wake leaves a token for a waiting reader (the channel holds one).
func (p *stdinPump) wake() {
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

func (p *stdinPump) run(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		p.mu.Lock()
		for len(p.data) >= pumpLimit {
			p.drained.Wait()
		}
		room := min(len(buf), pumpLimit-len(p.data))
		p.mu.Unlock()
		n, err := r.Read(buf[:room])
		p.mu.Lock()
		p.data = append(p.data, buf[:n]...)
		if err != nil {
			p.err = err
		}
		p.mu.Unlock()
		p.wake()
		if err != nil {
			return
		}
	}
}

// read copies pending bytes into buf, waiting for some until the timeout
// passes (forever when wait is false): the bytes, 0 at end of file, or
// -errno, with timedOut when nothing arrived in time.
func (p *stdinPump) read(buf []byte, timeout time.Duration, wait bool) int64 {
	var expire <-chan time.Time
	if wait {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expire = t.C
	}
	for {
		p.mu.Lock()
		if len(p.data) > 0 {
			n := copy(buf, p.data)
			p.data = p.data[n:]
			more := len(p.data) > 0 || p.err != nil
			p.drained.Signal()
			p.mu.Unlock()
			if more {
				p.wake() // another reader may be waiting for what is left
			}
			return int64(n)
		}
		err := p.err
		p.mu.Unlock()
		if err != nil {
			p.wake()
			if errors.Is(err, io.EOF) {
				return 0
			}
			return -int64(errnoOf(err))
		}
		select {
		case <-p.ready:
		case <-expire:
			return timedOut
		}
	}
}

// readTimeout is io_read_timeout on a file other than standard input. It
// first makes one non-blocking attempt, so ready data wins even over a
// zero timeout (an expired deadline fails a Read without trying it); then
// a pipe gets a read deadline for one read, which is cleared on return. A
// file that takes no deadline (a regular file, which never blocks) is
// read directly.
func readTimeout(f *os.File, buf []byte, timeout time.Duration) (int, error) {
	if rc, err := f.SyscallConn(); err == nil {
		var n int
		var rerr error
		cerr := rc.Read(func(fd uintptr) bool {
			for {
				n, rerr = syscall.Read(int(fd), buf)
				if !errors.Is(rerr, syscall.EINTR) {
					return true // one attempt, never wait here
				}
			}
		})
		if cerr == nil && !errors.Is(rerr, syscall.EAGAIN) {
			switch {
			case rerr != nil:
				return 0, rerr
			case n <= 0:
				return 0, io.EOF
			}
			return n, nil
		}
	}
	if err := f.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return f.Read(buf)
	}
	defer func() { _ = f.SetReadDeadline(time.Time{}) }()
	return f.Read(buf)
}
