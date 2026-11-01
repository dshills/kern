package interp

import (
	"math"
	"testing"
	"time"
)

// now_ns never goes backwards and is near the wall clock (specs/std-time R5).
func TestMonotonicNow(t *testing.T) {
	first := monotonicNow()
	if d := int64(first) - time.Now().UnixNano(); d > int64(time.Second) || d < -int64(time.Second) {
		t.Errorf("now_ns is %d ns from the wall clock", d)
	}
	prev := first
	for i := 0; i < 10000; i++ {
		n := monotonicNow()
		if n < prev {
			t.Fatalf("now_ns went back from %d to %d", prev, n)
		}
		prev = n
	}
}

// A u64 duration is slept in pieces that each fit a time.Duration.
func TestSleepChunks(t *testing.T) {
	if c := sleepChunks(0); len(c) != 0 {
		t.Errorf("0 ns: %v", c)
	}
	if c := sleepChunks(5); len(c) != 1 || c[0] != 5 {
		t.Errorf("5 ns: %v", c)
	}
	c := sleepChunks(math.MaxUint64)
	var sum uint64
	for _, d := range c {
		if d <= 0 { // a time.Duration is an int64, so a positive one fits
			t.Fatalf("chunk %d", d)
		}
		sum += uint64(d)
	}
	if len(c) != 3 || sum != math.MaxUint64 {
		t.Errorf("u64 max: %d chunks summing to %d", len(c), sum)
	}
}
