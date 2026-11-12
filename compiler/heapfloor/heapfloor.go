// Package heapfloor keeps Go's collector from running while this process's
// heap is small.
//
// The collector's default starts a cycle each time the heap has doubled
// since the last one, from a 4 MiB minimum. The compiler and, far more,
// the interpreter allocate gigabytes of short-lived values while a few
// megabytes stay live, so at the default they collect hundreds of times a
// second: 20 to 45% of everything the interpreter executed was the
// collector (docs/reports/perf-review.md, 5.1). A fixed larger percentage
// would cure that and cost a program with a large live heap several times
// its size; a floor does not: the heap may reach the floor before a cycle
// starts, and above the floor the default's doubling rule is back.
package heapfloor

import (
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"
)

// Default is the floor the kern command and the tests use.
const Default = 64 << 20

// minHeap is the collector's own minimum heap at 100 percent.
const minHeap = 4 << 20

var once sync.Once

// Set makes the heap grow to floor bytes before a collection starts,
// whatever is live, and leaves the default behaviour above that. The first
// call wins. It does nothing when GOGC or GOMEMLIMIT is in the
// environment: whoever set one of them chose.
func Set(floor uint64) {
	if os.Getenv("GOGC") != "" || os.Getenv("GOMEMLIMIT") != "" || floor <= minHeap {
		return
	}
	once.Do(func() {
		debug.SetGCPercent(percent(floor, 0))
		arm(floor)
	})
}

// sentinel is an object whose finalizer runs after each collection: big
// enough not to share a block with other small objects, which would delay
// the finalizer.
type sentinel struct{ _ [64]byte }

// arm makes the percentage follow the live heap: a finalizer runs once its
// object is found dead, which is at the next cycle, and makes the next
// object.
func arm(floor uint64) {
	s := new(sentinel)
	runtime.SetFinalizer(s, func(*sentinel) {
		debug.SetGCPercent(percent(floor, live()))
		arm(floor)
	})
}

// live is what the last collection found reachable.
func live() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}

// percent is the collector setting under which the next cycle starts when
// the heap reaches floor, given what is live, and never less than the
// default's 100. The collector's goal is the larger of live·(1+p/100) and
// minHeap·p/100; the smaller of the two p that reach floor keeps both at
// or under it.
func percent(floor, live uint64) int {
	p := floor * 100 / minHeap
	if live > 0 && live < floor {
		if q := (floor - live) * 100 / live; q < p {
			p = q
		}
	}
	if live >= floor || p < 100 {
		p = 100
	}
	return int(p)
}
