package interp

import (
	"math"
	"sync"
	"time"
)

// clockStart is the interpreter's first clock reading: the wall clock as
// a number, and the time.Time whose monotonic reading later ones are
// measured from.
var clockStart struct {
	once sync.Once
	at   time.Time
	wall uint64
}

// monotonicNow is now_ns: the wall clock at the first reading plus the
// monotonic time since, so it never goes backwards and stays in the wall
// clock's range (specs/std-time R5).
func monotonicNow() uint64 {
	clockStart.once.Do(func() {
		clockStart.at = time.Now()
		clockStart.wall = uint64(clockStart.at.UnixNano())
	})
	return clockStart.wall + uint64(time.Since(clockStart.at))
}

// sleepChunks splits a u64 nanosecond duration into time.Duration pieces,
// none above math.MaxInt64 (time_sleep_ns).
func sleepChunks(ns uint64) []time.Duration {
	var out []time.Duration
	for ns > 0 {
		d := ns
		if d > math.MaxInt64 {
			d = math.MaxInt64
		}
		out = append(out, time.Duration(d))
		ns -= d
	}
	return out
}
