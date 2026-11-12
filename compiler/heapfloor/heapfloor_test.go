package heapfloor

import "testing"

func TestPercent(t *testing.T) {
	const mb = 1 << 20
	cases := []struct {
		floor, live uint64
		want        int
	}{
		{64 * mb, 0, 1600},       // nothing measured yet: the minimum heap rule alone
		{64 * mb, 1 * mb, 1600},  // a small live heap: still that rule
		{64 * mb, 5 * mb, 1180},  // 5 MiB live: 5·(1+11.8) = 64
		{64 * mb, 32 * mb, 100},  // half the floor: the default
		{64 * mb, 40 * mb, 100},  // never below the default
		{64 * mb, 64 * mb, 100},  // at the floor
		{64 * mb, 500 * mb, 100}, // far above it
		{64 * mb, 16 * mb, 300},  // 16·4 = 64
	}
	for _, c := range cases {
		if got := percent(c.floor, c.live); got != c.want {
			t.Errorf("percent(%d MiB, %d MiB) = %d, want %d", c.floor/mb, c.live/mb, got, c.want)
		}
		// the goal the collector derives never passes the floor while the
		// percentage is above the default
		p := uint64(percent(c.floor, c.live))
		goal := c.live + c.live*p/100
		if m := minHeap * p / 100; m > goal {
			goal = m
		}
		if p > 100 && goal > c.floor {
			t.Errorf("percent(%d MiB, %d MiB) = %d gives a goal of %d bytes, over the floor", c.floor/mb, c.live/mb, p, goal)
		}
	}
}
