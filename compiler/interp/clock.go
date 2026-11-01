package interp

import (
	"math"
	"strconv"
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

// f64Digits is str_f64_digits: the fewest correctly rounded significant
// digits, 1 to 17, that read back as v, in exponent form, the same search
// the native runtime makes with snprintf and strtod (specs/float-text).
func f64Digits(v float64) string {
	var s string
	for p := 1; p <= 17; p++ {
		s = strconv.FormatFloat(v, 'e', p-1, 64)
		if r, err := strconv.ParseFloat(s, 64); err == nil && r == v {
			break
		}
	}
	return s
}

// parseF64 is str_parse_f64: the correctly rounded value of text already
// checked to be one JSON number; a range error carries ±Inf.
func parseF64(b []byte) float64 {
	v, _ := strconv.ParseFloat(string(b), 64)
	return v
}
