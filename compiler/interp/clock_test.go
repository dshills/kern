package interp

import (
	"math"
	"strconv"
	"strings"
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

// f64Digits gives the round-trip digits: they read back bitwise, no fewer
// correctly rounded digits do, and they are at most one longer than the
// strict shortest string (specs/float-text R2).
func TestF64Digits(t *testing.T) {
	seed := uint64(0x9e3779b97f4a7c15)
	checked, longer := 0, 0
	for checked < 10000 {
		seed = seed*6364136223846793005 + 1442695040888963407
		v := math.Float64frombits(seed)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		checked++
		s := f64Digits(v)
		r, err := strconv.ParseFloat(s, 64)
		if err != nil || math.Float64bits(r) != math.Float64bits(v) {
			t.Fatalf("%v: %s reads back as %v", v, s, r)
		}
		mant := strings.SplitN(strings.TrimPrefix(s, "-"), "e", 2)[0]
		p := len(strings.ReplaceAll(mant, ".", ""))
		if p > 1 {
			short := strconv.FormatFloat(v, 'e', p-2, 64)
			if back, err := strconv.ParseFloat(short, 64); err == nil && back == v {
				t.Fatalf("%v: %d digits read back, but %s was chosen", v, p-1, s)
			}
		}
		strict := strings.SplitN(strings.TrimPrefix(strconv.FormatFloat(v, 'e', -1, 64), "-"), "e", 2)[0]
		switch d := p - len(strings.ReplaceAll(strict, ".", "")); {
		case d == 1:
			longer++
		case d != 0:
			t.Fatalf("%v: %s has %d more digits than %s", v, s, d, strict)
		}
	}
	t.Logf("%d values: %d one digit longer than the strict shortest", checked, longer)
}

// 2^-24 is the known case where a shorter string reads back but is not the
// correctly rounded one at its length, so the round-trip digits are one
// longer than the strict shortest (specs/float-text glossary).
func TestF64DigitsLongerCase(t *testing.T) {
	if got := f64Digits(math.Ldexp(1, -24)); got != "5.9604644775390625e-08" {
		t.Errorf("2^-24: %s", got)
	}
}
