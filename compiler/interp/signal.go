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
// The interpreter is a Go program, and the Go runtime has its own way
// with signals: left alone it ends the program on Interrupt, Terminate
// and Hangup, as the system does, writes a stack dump on Quit, and lets
// User1, User2 and Pipe pass, where the system would end the program.
// When the interpreted program is all the process is for (`kern run`,
// SetOwnsProcess) the interpreter gives those four the system's default
// (specs/runtime RM6). Quit, User1 and User2 it asks for, and one that
// the program neither catches nor ignores ends the process by that
// signal; Pipe cannot be asked for (see below), so it is the program's
// own raise of it that ends the process. A program run inside another
// process (a test) leaves the process's signals alone.
//
// An ignored signal is one the interpreter receives and drops, not one
// the system discards: a disposition of "ignored" set in the system
// would pass to every child, which std/process promises a child does not
// see. And the Go runtime keeps a Pipe signal that another process
// sends, whatever was asked: a program sees its own raise of Pipe, and a
// write to a closed pipe or socket is an error here as it is natively.

var sigNumbers = [...]syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGPIPE, syscall.SIGCHLD}

var sigs struct {
	mu      sync.Mutex
	live    bool
	held    uint32
	pending uint32
	ignored uint32 // the signals the program ignores
	ch      chan os.Signal
	wake    chan struct{}
	own     bool           // the program owns the process's signals
	dflt    chan os.Signal // the signals asked for so as to give them the system's default
	// pipe is the runtime's signal pipe (rt_signal.c): a byte is written
	// for every arrival recorded, so that its read end is readable while
	// one waits and a Poller can watch it (specs/std-streams R9). It is
	// made at the first catch and kept for the life of the process.
	pipe    [2]int
	hasPipe bool
}

// sigNote writes the byte that says "look at the bits"; a full pipe
// already says it. The caller holds sigs.mu.
func sigNote() {
	if sigs.hasPipe {
		_, _ = syscall.Write(sigs.pipe[1], []byte{0})
	}
}

// sigDrain empties the pipe. The caller holds sigs.mu.
func sigDrain() {
	if !sigs.hasPipe {
		return
	}
	var buf [64]byte
	for {
		n, err := syscall.Read(sigs.pipe[0], buf[:])
		if n <= 0 || err != nil {
			return
		}
	}
}

// signalFD is the runtime's kern_signal_fd: a descriptor of the program's
// that stands for the pipe's read end, the same one every time.
func signalFD(it *Interp, _ []Value, _ callCtx) []Value {
	sigs.mu.Lock()
	ok, fd := sigs.hasPipe, sigs.pipe[0]
	sigs.mu.Unlock()
	if !ok {
		return i32Val(-1)
	}
	it.sh.sigSockOnce.Do(func() {
		it.sh.sigSock = it.addSock(&sock{raw: fd, hasRaw: true, signal: true})
	})
	return i32Val(int32(it.sh.sigSock))
}

// sigFatal are the signals whose default the interpreter must supply:
// the Go runtime's own default for them is not the system's.
var sigFatal = []os.Signal{syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGQUIT}

// defaultSignals makes the system's default the default of the signals
// the Go runtime would treat otherwise, for a program that owns the
// process.
func defaultSignals() {
	sigs.mu.Lock()
	defer sigs.mu.Unlock()
	if sigs.dflt == nil {
		sigs.dflt = make(chan os.Signal, 8)
		go func() {
			for s := range sigs.dflt {
				sigs.mu.Lock()
				fatal := sigs.own
				for i, n := range sigNumbers {
					if s == n && (sigs.held|sigs.ignored)&(1<<i) != 0 {
						fatal = false
					}
				}
				sigs.mu.Unlock()
				if fatal {
					dieBySignal(s.(syscall.Signal))
				}
			}
		}()
	}
	sigs.own = true
	signal.Notify(sigs.dflt, sigFatal...)
}

// dieBySignal ends the process as the system's default for the signal
// does: the signal gets that default back from the Go runtime and is
// sent. Should that fail, the process exits with the status a shell
// gives a program a signal ended.
func dieBySignal(sig syscall.Signal) {
	signal.Reset(sig)
	if setSigDefault(sig) == nil && syscall.Kill(syscall.Getpid(), sig) == nil {
		time.Sleep(time.Second) // the signal ends the process before this returns
	}
	os.Exit(128 + int(sig))
}

// ownDefault reports whether the signal is one whose default the
// interpreter itself supplies for this program. Such a signal is always
// asked for through sigs.dflt, and ignoring it is a bit the reader of
// that channel consults: no change of what the Go runtime is asked
// leaves a moment in which the signal would pass.
func ownDefault(sig syscall.Signal) bool {
	if !sigs.own {
		return false
	}
	for _, s := range sigFatal {
		if s == sig {
			return true
		}
	}
	return false
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
		"signal_fd":    signalFD,
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
	if sigs.dflt != nil {
		signal.Stop(sigs.dflt)
	}
	sigs.held, sigs.pending, sigs.ignored, sigs.live, sigs.own = 0, 0, 0, false, false
	sigDrain()
}

func signalOpen(_ *Interp, _ []Value, _ callCtx) []Value {
	sigs.mu.Lock()
	defer sigs.mu.Unlock()
	if sigs.live {
		return i32Val(-int32(syscall.EBUSY))
	}
	if !sigs.hasPipe {
		var p [2]int
		if err := syscall.Pipe(p[:]); err != nil {
			return i32Val(-int32(errnoOf(err)))
		}
		for _, fd := range p {
			syscall.CloseOnExec(fd)
			_ = syscall.SetNonblock(fd, true)
		}
		sigs.pipe, sigs.hasPipe = p, true
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
						sigNote()
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
	sigDrain()
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
		sigs.ignored |= 1 << i
		if !ownDefault(sigNumbers[i]) {
			signal.Notify(sigDrop, sigNumbers[i])
		}
	} else {
		sigs.ignored &^= 1 << i
		if !ownDefault(sigNumbers[i]) {
			signal.Reset(sigNumbers[i]) // no channel asks for it any more
		}
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
		// Nothing wanted is recorded, which is to say nothing is: one
		// Signals exists at a time and its mask is every signal held. The
		// pipe is emptied, so that it is readable only for an arrival
		// recorded from here on. The lock that guards the record also
		// guards the byte written for it, so no arrival lies between
		// this look and the emptying.
		sigDrain()
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
	sigs.mu.Lock()
	held := sigs.held&(1<<i) != 0
	fatal := sigs.own && !held && sigs.ignored&(1<<i) == 0 && sigNumbers[i] != syscall.SIGCHLD
	sigs.mu.Unlock()
	if fatal {
		// the system's default ends the program before raise returns
		dieBySignal(sigNumbers[i])
	}
	if sigNumbers[i] == syscall.SIGPIPE {
		// The Go runtime keeps a SIGPIPE that kill sends: it reaches no
		// channel. A raise of a caught Pipe is recorded here instead.
		sigs.mu.Lock()
		if held {
			sigs.pending |= 1 << i
			sigNote()
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
