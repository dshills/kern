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
// strconv.ParseFloat stops reading an exponent's digits near 10000 before
// it adds the digit position, so a long digit run that cancels a large
// exponent (`0.` 100,001 zeros `1e100002`) would read wrong; the text is
// rewritten by canonicalNumber when it is over 800 bytes, which rounds
// the same way (specs/freestanding-float R9).
func parseF64(b []byte) float64 {
	text := string(b)
	if len(b) > 800 {
		// both defects need more than 800 bytes: an integer part over 800
		// digits, or a digit run long enough to cancel an exponent near
		// 10000 (within 800 digits such a value is far out of range)
		text = canonicalNumber(b)
	}
	v, _ := strconv.ParseFloat(text, 64)
	return v
}

// canonicalNumber rewrites one JSON number as its significant digits, at
// most 800 and a sticky 801st digit 1 when any dropped digit is nonzero (a
// halfway point between doubles has at most 767), written d.ddd with the
// exponent of the first; a value whose leading digit is above 10^309 becomes
// 1e400 and one below 10^-324 becomes 0, with the sign. The digit scan
// and bounds are the freestanding runtime's (runtime/freestanding/src/
// f64text.c).
func canonicalNumber(p []byte) string {
	const keep = 800
	var dig []byte
	sticky := false
	i, n := 0, len(p)
	var eadj int64
	sign := ""
	if i < n && p[i] == '-' {
		sign = "-"
		i++
	}
	for ; i < n && p[i] >= '0' && p[i] <= '9'; i++ {
		switch c := p[i]; {
		case len(dig) == 0 && c == '0':
		case len(dig) < keep:
			dig = append(dig, c)
		default:
			sticky = sticky || c != '0'
			eadj++
		}
	}
	if i < n && p[i] == '.' {
		for i++; i < n && p[i] >= '0' && p[i] <= '9'; i++ {
			switch c := p[i]; {
			case len(dig) == 0 && c == '0':
				eadj--
			case len(dig) < keep:
				dig = append(dig, c)
				eadj--
			default:
				sticky = sticky || c != '0'
			}
		}
	}
	var ex int64
	if i < n && (p[i] == 'e' || p[i] == 'E') {
		i++
		neg := false
		if i < n && (p[i] == '+' || p[i] == '-') {
			neg = p[i] == '-'
			i++
		}
		for ; i < n && p[i] >= '0' && p[i] <= '9'; i++ {
			if ex < 100000000000000000 {
				ex = ex*10 + int64(p[i]-'0')
			}
		}
		if neg {
			ex = -ex
		}
	}
	if len(dig) == 0 {
		return sign + "0"
	}
	if sticky {
		dig = append(dig, '1')
		eadj--
	}
	e := eadj + ex
	switch lead := int64(len(dig)) - 1 + e; {
	case lead > 309:
		return sign + "1e400"
	case lead < -324:
		return sign + "0"
	}
	// d.ddd form: strconv drops the integer digits past 800 from the
	// decimal point's position when it falls back to its slow path (an
	// 801-digit integer near a halfway point reads 10 times too small)
	text := string(dig[:1])
	if len(dig) > 1 {
		text += "." + string(dig[1:])
	}
	return sign + text + "e" + strconv.FormatInt(e+int64(len(dig))-1, 10)
}
