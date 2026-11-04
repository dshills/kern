package interp

import (
	"errors"
	"io"
	"testing"
	"time"
)

// The stdin pump (specs/std-timeouts §2): a timed read with nothing
// pending times out and consumes nothing; bytes that arrive later go to
// the next read, timed or not, in order, none lost or repeated; a read
// takes at most its buffer and leaves the rest pending; end of file is 0
// for every later read; a read error is -errno after the pending bytes.
func TestStdinPump(t *testing.T) {
	r, w := io.Pipe()
	p := newStdinPump()
	go p.run(r)
	buf := make([]byte, 4)
	if n := p.read(buf, 50*time.Millisecond, true); n != timedOut {
		t.Fatalf("empty pump: %d, want timed out", n)
	}
	go func() { _, _ = w.Write([]byte("abcdef")) }()
	if n := p.read(buf, 10*time.Second, true); n != 4 || string(buf[:n]) != "abcd" {
		t.Fatalf("first read: %d %q", n, buf[:max(n, 0)])
	}
	if n := p.read(buf, 0, false); n != 2 || string(buf[:n]) != "ef" {
		t.Fatalf("untimed read of the rest: %d %q", n, buf[:max(n, 0)])
	}
	if n := p.read(buf, 0, true); n != timedOut {
		t.Fatalf("zero timeout, nothing pending: %d", n)
	}
	_ = w.Close()
	for i := 0; i < 2; i++ {
		if n := p.read(buf, 10*time.Second, true); n != 0 {
			t.Fatalf("end of file read %d: %d", i, n)
		}
	}

	r2, w2 := io.Pipe()
	q := newStdinPump()
	go q.run(r2)
	go func() {
		_, _ = w2.Write([]byte("x"))
		_ = w2.CloseWithError(errors.New("broken"))
	}()
	if n := q.read(buf, 10*time.Second, true); n != 1 {
		t.Fatalf("bytes before the error: %d", n)
	}
	if n := q.read(buf, 10*time.Second, true); n >= 0 || n == timedOut {
		t.Fatalf("after the error: %d, want -errno", n)
	}
}

// The pump reads ahead at most pumpLimit bytes while the program reads
// nothing; once the program reads, the rest arrives whole.
func TestStdinPumpBounded(t *testing.T) {
	r, w := io.Pipe()
	p := newStdinPump()
	go p.run(r)
	const total = 1 << 20
	go func() {
		chunk := make([]byte, 1000)
		for sent := 0; sent < total; sent += len(chunk) {
			for i := range chunk {
				chunk[i] = byte(sent + i)
			}
			_, _ = w.Write(chunk[:min(len(chunk), total-sent)])
		}
		_ = w.Close()
	}()
	time.Sleep(200 * time.Millisecond)
	p.mu.Lock()
	held := len(p.data)
	p.mu.Unlock()
	if held > pumpLimit {
		t.Fatalf("pump holds %d bytes unread, want at most %d", held, pumpLimit)
	}
	got := 0
	buf := make([]byte, 8192)
	for {
		n := p.read(buf, 10*time.Second, true)
		if n < 0 {
			t.Fatalf("read: %d", n)
		}
		if n == 0 {
			break
		}
		for i := 0; i < int(n); i++ {
			if buf[i] != byte(got+i) {
				t.Fatalf("byte %d is %d", got+i, buf[i])
			}
		}
		got += int(n)
	}
	if got != total {
		t.Fatalf("read %d bytes, want %d", got, total)
	}
}

// Two readers waiting on the pump both get bytes from one write: the
// first leaves a wake-up for the second while bytes remain.
func TestStdinPumpTwoReaders(t *testing.T) {
	r, w := io.Pipe()
	p := newStdinPump()
	go p.run(r)
	got := make(chan int64, 2)
	for i := 0; i < 2; i++ {
		go func() {
			buf := make([]byte, 8)
			got <- p.read(buf, 0, false)
		}()
	}
	time.Sleep(50 * time.Millisecond) // both are waiting
	go func() { _, _ = w.Write([]byte("0123456789abcdef")) }()
	for i := 0; i < 2; i++ {
		select {
		case n := <-got:
			if n != 8 {
				t.Errorf("reader got %d bytes, want 8", n)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a reader never woke although bytes were pending")
		}
	}
	_ = w.Close()
}
