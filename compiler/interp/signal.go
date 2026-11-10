package interp

import (
	"crypto/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// std/signal and std/rand (specs/runtime RI1, RI2), as the hosted
// runtime's rt_signal.c and rt_rand.c do them.
//
// Signals belong to the process, so their state is the package's, not an
// interpreter's. A caught signal is recorded as a bit; os/signal delivers
// to a channel that one goroutine turns into bits.
//
// The interpreter is a Go program, and three things follow. A signal left
// at its default does what the Go runtime does with it: Interrupt,
// Terminate and Hangup end the program, Quit ends it with the runtime's
// stack dump, and User1, User2 and Pipe do nothing, where the system
// would end the program. A Pipe signal sent by another process is kept
// by the Go runtime and never reported. And an ignored signal is one the
// interpreter receives and drops, not one the system discards: a
// disposition of "ignored" set in the system would pass to every child,
// which std/process promises a child does not see.

var sigNumbers = [...]syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGPIPE, syscall.SIGCHLD}

var sigs struct {
	mu      sync.Mutex
	live    bool
	held    uint32
	pending uint32
	ch      chan os.Signal
	wake    chan struct{}
}

// sigDrop receives the signals the program ignores; nothing reads it, and
// os/signal drops what a full channel cannot take.
var sigDrop = make(chan os.Signal, 1)

func init() {
	for k, v := range map[string]func(*Interp, []Value, callCtx) []Value{
		"signal_open":  signalOpen,
		"signal_catch": signalCatch,
		"signal_close": signalClose,
		"signal_set":   signalSet,
		"signal_wait":  signalWait,
		"signal_raise": signalRaise,
		"rand_fill":    randFill,
	} {
		intrinsics[k] = v
	}
}

// releaseSignals ends what a program did to the process's signals: its
// caught signals and its ignored ones go back to the Go runtime's
// default, and a later program in this process starts clean.
func releaseSignals() {
	sigs.mu.Lock()
	defer sigs.mu.Unlock()
	if sigs.ch != nil {
		signal.Stop(sigs.ch)
	}
	signal.Stop(sigDrop)
	sigs.held, sigs.pending, sigs.live = 0, 0, false
}

func signalOpen(_ *Interp, _ []Value, _ callCtx) []Value {
	sigs.mu.Lock()
	defer sigs.mu.Unlock()
	if sigs.live {
		return i32Val(-int32(syscall.EBUSY))
	}
	sigs.live = true
	if sigs.ch == nil {
		sigs.ch = make(chan os.Signal, 64)
		sigs.wake = make(chan struct{}, 1)
		go func() {
			for s := range sigs.ch {
				sigs.mu.Lock()
				for i, n := range sigNumbers {
					if s == n && sigs.held&(1<<i) != 0 {
						sigs.pending |= 1 << i
					}
				}
				sigs.mu.Unlock()
				select {
				case sigs.wake <- struct{}{}:
				default:
				}
			}
		}()
	}
	return i32Val(0)
}

func signalCatch(_ *Interp, a []Value, _ callCtx) []Value {
	i := int(u64(a[0]))
	if i < 0 || i >= len(sigNumbers) {
		return i32Val(-int32(syscall.EINVAL))
	}
	sigs.mu.Lock()
	defer sigs.mu.Unlock()
	if sigs.held&(1<<i) != 0 {
		return i32Val(0)
	}
	signal.Notify(sigs.ch, sigNumbers[i])
	sigs.held |= 1 << i
	return i32Val(0)
}

func signalClose(_ *Interp, _ []Value, _ callCtx) []Value {
	sigs.mu.Lock()
	defer sigs.mu.Unlock()
	// a signal goes back to what it was: dropped, when the program had
	// ignored it (sigDrop still asks for it), else the Go runtime's default
	if sigs.ch != nil {
		signal.Stop(sigs.ch)
	}
	sigs.held, sigs.pending, sigs.live = 0, 0, false
	return nil
}

func signalSet(_ *Interp, a []Value, _ callCtx) []Value {
	i, how := int(u64(a[0])), int32(a[1].(Int).Bits)
	if i < 0 || i >= len(sigNumbers) || how < 0 || how > 1 {
		return i32Val(-int32(syscall.EINVAL))
	}
	sigs.mu.Lock()
	defer sigs.mu.Unlock()
	if sigs.held&(1<<i) != 0 {
		return i32Val(-int32(syscall.EBUSY))
	}
	if how == 1 {
		signal.Notify(sigDrop, sigNumbers[i])
	} else {
		signal.Reset(sigNumbers[i]) // no channel asks for it any more
	}
	return i32Val(0)
}

// signalWait returns the first recorded signal of the mask, in Signal's
// order, and forgets it; it waits for one, at most the given time when
// timed.
func signalWait(_ *Interp, a []Value, _ callCtx) []Value {
	mask := uint32(a[0].(Int).Bits)
	timed := bool(a[2].(Bool))
	var end time.Time
	if timed {
		end = time.Now().Add(time.Duration(min(a[1].(Int).Bits, uint64(1<<62))))
	}
	for {
		sigs.mu.Lock()
		for i := range sigNumbers {
			if bit := uint32(1) << i; sigs.pending&mask&bit != 0 {
				sigs.pending &^= bit
				sigs.mu.Unlock()
				return i64Val(int64(i))
			}
		}
		wake := sigs.wake
		sigs.mu.Unlock()
		if !timed {
			<-wake
			continue
		}
		left := time.Until(end)
		if left <= 0 {
			return i64Val(timedOut)
		}
		t := time.NewTimer(left)
		select {
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}

func signalRaise(_ *Interp, a []Value, _ callCtx) []Value {
	i := int(u64(a[0]))
	if i < 0 || i >= len(sigNumbers) {
		return i32Val(-int32(syscall.EINVAL))
	}
	if sigNumbers[i] == syscall.SIGPIPE {
		// The Go runtime keeps a SIGPIPE that kill sends: it reaches no
		// channel. A raise of a caught Pipe is recorded here instead; one
		// that is not caught does nothing, where the system's default
		// would end the program.
		sigs.mu.Lock()
		held := sigs.held&(1<<i) != 0
		if held {
			sigs.pending |= 1 << i
		}
		wake := sigs.wake
		sigs.mu.Unlock()
		if held {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		return i32Val(0)
	}
	if err := syscall.Kill(syscall.Getpid(), sigNumbers[i]); err != nil {
		return i32Val(-int32(errnoOf(err)))
	}
	return i32Val(0)
}

// randFill fills the buffer from the system's generator in requests of at
// most 256 bytes, as the runtime does, so that testing.fail_random fails
// the same request in both engines.
func randFill(it *Interp, a []Value, _ callCtx) []Value {
	seq := byteSeq(acc(a[0]))
	buf := make([]byte, seq.length())
	for at := 0; at < len(buf); at += 256 {
		it.sh.mu.Lock()
		fail := it.sh.failRandom == 1
		if it.sh.failRandom > 0 {
			it.sh.failRandom--
		}
		it.sh.mu.Unlock()
		if fail {
			return i32Val(-int32(syscall.EIO))
		}
		if _, err := rand.Read(buf[at:min(at+256, len(buf))]); err != nil {
			return i32Val(-int32(syscall.EIO))
		}
	}
	for i, b := range buf {
		seq.at(i).set(mkInt(tyU8, uint64(b)))
	}
	return i32Val(0)
}
