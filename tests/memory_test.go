package tests

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/native"
)

// The allocation report of KERN_ALLOC_REPORT=1 (specs/runtime RF2): one
// line on standard error when the program returns from main or calls exit.
// The checks of RF1 are trap fixtures (tests/trap/sysalloc_*.kern).

type allocSite struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Count uint64 `json:"count"`
	Bytes uint64 `json:"bytes"`
}

type allocReport struct {
	Alloc struct {
		Total     uint64      `json:"total"`
		Live      uint64      `json:"live"`
		LiveBytes uint64      `json:"live_bytes"`
		PeakBytes uint64      `json:"peak_bytes"`
		Sites     []allocSite `json:"sites"`
	} `json:"alloc"`
}

// runReport runs exe and returns its standard error and exit status.
func runReport(t *testing.T, exe string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(exe)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	status := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run: %v", err)
		}
		status = ee.ExitCode()
	}
	return eb.String(), status
}

// parseReport reads the one line the report is: the shape the spec gives,
// with no field beyond it.
func parseReport(t *testing.T, stderr string) allocReport {
	t.Helper()
	if strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, "\n") {
		t.Fatalf("standard error is not one line: %q", stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stderr))
	dec.DisallowUnknownFields()
	var r allocReport
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("report %q: %v", stderr, err)
	}
	if r.Alloc.Sites == nil {
		t.Fatalf("report has no sites array: %q", stderr)
	}
	return r
}

// lineOf is the 1-based line of the first occurrence of marker in src.
func lineOf(t *testing.T, src, marker string) int {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("no %q in the program", marker)
	}
	return 1 + strings.Count(src[:i], "\n")
}

const reportProgram = `module t
func worker(n: u64) -> u64 { return n + 1u64 }
// objects makes a Shared, a Mutex, a vector's buffer and a thread, and
// destroys them all: four allocations, none of them live afterwards.
func objects() -> Result[u64, SpawnErr] {
    s := Shared[u64].new(5u64)
    m := Mutex[u64].new(6u64)
    v := Vec[u64].new()
    v.push(1u64)
    t := thread.spawn(worker, 1u64)?
    return Ok(t.join())
}
contract { requires "the blocks left allocated are the report's subject" }
func main() -> i32 {
    match objects() {
        Ok(n) => { }
        Err(e) => { }
    }
    a := SystemAllocator.new()
    unsafe {
        match a.alloc(4096usize, 8usize) { // KEPT
            Ok(p) => { }
            Err(e) => { }
        }
        match a.alloc(64usize, 8usize) {
            Ok(p) => { a.free(p, 64usize, 8usize) }
            Err(e) => { }
        }
        for i in 0usize..3usize {
            match a.alloc_zeroed(128usize, 8usize) { // THREE
                Ok(p) => { }
                Err(e) => { }
            }
        }
    }
    return 0i32
}
`

func TestAllocReport(t *testing.T) {
	exe := buildProgram(t, reportProgram, native.Options{})
	t.Run("off unless asked for", func(t *testing.T) {
		for _, env := range []string{"", "KERN_ALLOC_REPORT=0", "KERN_ALLOC_REPORT="} {
			if got, status := runReport(t, exe, env); got != "" || status != 0 {
				t.Errorf("%q: standard error %q, status %d", env, got, status)
			}
		}
	})
	t.Run("the counters and the sites", func(t *testing.T) {
		stderr, status := runReport(t, exe, "KERN_ALLOC_REPORT=1")
		if status != 0 {
			t.Fatalf("status %d, stderr %q", status, stderr)
		}
		r := parseReport(t, stderr).Alloc
		// objects: Shared, Mutex, the vector's buffer, the thread; main:
		// 4096 kept, 64 freed, three of 128 kept
		if r.Total != 9 || r.Live != 4 || r.LiveBytes != 4096+3*128 || r.PeakBytes != 4096+3*128 {
			t.Errorf("total %d live %d live_bytes %d peak_bytes %d, want 9 4 4480 4480", r.Total, r.Live, r.LiveBytes, r.PeakBytes)
		}
		want := []allocSite{
			{"t.kern", lineOf(t, reportProgram, "// KEPT"), 1, 4096},
			{"t.kern", lineOf(t, reportProgram, "// THREE"), 3, 384},
		}
		if fmt.Sprint(r.Sites) != fmt.Sprint(want) {
			t.Errorf("sites %v, want %v", r.Sites, want)
		}
	})
	t.Run("a release build writes nothing", func(t *testing.T) {
		rel := buildProgram(t, reportProgram, native.Options{Opt: "release"})
		if got, status := runReport(t, rel, "KERN_ALLOC_REPORT=1"); got != "" || status != 0 {
			t.Errorf("standard error %q, status %d", got, status)
		}
	})
}

// A program that ends by exit is reported too, with what was live at the
// call: destructors do not run. A block the library allocates belongs to
// the user's call, and a runtime object to the call that made it.
const reportExitProgram = `module t
func main() -> i32 {
    v := Vec[u64].new()
    v.push(1u64) // PUSH
    s := Shared[u64].new(5u64) // SHARED
    exit(3i32)
    return 0i32
}
`

func TestAllocReportAtExit(t *testing.T) {
	exe := buildProgram(t, reportExitProgram, native.Options{})
	stderr, status := runReport(t, exe, "KERN_ALLOC_REPORT=1")
	if status != 3 {
		t.Fatalf("status %d, want 3; stderr %q", status, stderr)
	}
	r := parseReport(t, stderr).Alloc
	if r.Total != 2 || r.Live != 2 || len(r.Sites) != 2 {
		t.Fatalf("total %d live %d sites %v, want 2 2 and two sites", r.Total, r.Live, r.Sites)
	}
	lines := map[int]uint64{}
	for _, s := range r.Sites {
		if s.File != "t.kern" {
			t.Errorf("site %v is not in the program's file", s)
		}
		lines[s.Line] = s.Count
	}
	for _, m := range []string{"// PUSH", "// SHARED"} {
		if lines[lineOf(t, reportExitProgram, m)] != 1 {
			t.Errorf("no site at the line of %s: %v", m, r.Sites)
		}
	}
}

// A reallocated block stays with the call that allocated it, at its new
// size; total counts the reallocation only when the block moved.
const reportReallocProgram = `module t
contract { requires "the block left allocated is the report's subject" }
func main() -> i32 {
    a := SystemAllocator.new()
    unsafe {
        match a.alloc(16usize, 8usize) { // FIRST
            Ok(p) => {
                match a.realloc(p, 16usize, 1048576usize, 8usize) {
                    Ok(q) => { }
                    Err(e) => { }
                }
            }
            Err(e) => { }
        }
    }
    return 0i32
}
`

func TestAllocReportRealloc(t *testing.T) {
	exe := buildProgram(t, reportReallocProgram, native.Options{})
	stderr, _ := runReport(t, exe, "KERN_ALLOC_REPORT=1")
	r := parseReport(t, stderr).Alloc
	if r.Total < 1 || r.Total > 2 || r.Live != 1 || r.LiveBytes != 1048576 || r.PeakBytes != 1048576 {
		t.Errorf("total %d live %d live_bytes %d peak_bytes %d, want 1 or 2, 1, 1048576, 1048576", r.Total, r.Live, r.LiveBytes, r.PeakBytes)
	}
	want := []allocSite{{"t.kern", lineOf(t, reportReallocProgram, "// FIRST"), 1, 1048576}}
	if fmt.Sprint(r.Sites) != fmt.Sprint(want) {
		t.Errorf("sites %v, want %v", r.Sites, want)
	}
}

// Twenty sites: the report lists the sixteen holding the most bytes,
// largest first, sites of equal bytes by line.
func TestAllocReportSiteOrder(t *testing.T) {
	var b strings.Builder
	b.WriteString("module t\ncontract { requires \"the blocks left allocated are the report's subject\" }\nfunc main() -> i32 {\n    a := SystemAllocator.new()\n    unsafe {\n")
	var want []allocSite
	line := 6
	for k := 0; k < 20; k++ {
		bytes := uint64(16 * (k%10 + 1)) // every size twice: ties
		fmt.Fprintf(&b, "        match a.alloc(%dusize, 8usize) {\n            Ok(p) => { }\n            Err(e) => { }\n        }\n", bytes)
		want = append(want, allocSite{"t.kern", line, 1, bytes})
		line += 4
	}
	b.WriteString("    }\n    return 0i32\n}\n")
	sort.SliceStable(want, func(i, j int) bool {
		if want[i].Bytes != want[j].Bytes {
			return want[i].Bytes > want[j].Bytes
		}
		return want[i].Line < want[j].Line
	})
	exe := buildProgram(t, b.String(), native.Options{})
	stderr, _ := runReport(t, exe, "KERN_ALLOC_REPORT=1")
	r := parseReport(t, stderr).Alloc
	if r.Total != 20 || r.Live != 20 {
		t.Errorf("total %d live %d, want 20 20", r.Total, r.Live)
	}
	if fmt.Sprint(r.Sites) != fmt.Sprint(want[:16]) {
		t.Errorf("sites\n %v\nwant\n %v", r.Sites, want[:16])
	}
}

// Four threads allocate and free at once: the counters lose nothing, and
// what each thread keeps is live at the end.
const reportThreadsProgram = `module t
contract { requires "the ten blocks each worker leaves allocated are the report's subject" }
func worker(n: u64) -> u64 {
    a := SystemAllocator.new()
    unsafe {
        for i in 0u64..n {
            match a.alloc(64usize, 8usize) {
                Ok(p) => { a.free(p, 64usize, 8usize) }
                Err(e) => { }
            }
        }
        for i in 0u64..10u64 {
            match a.alloc(32usize, 8usize) { // KEPT
                Ok(p) => { }
                Err(e) => { }
            }
        }
    }
    return n
}
func run() -> Result[u64, SpawnErr] {
    var sum: u64 = 0u64
    with s := thread.scope() {
        a := s.spawn(worker, 1000u64)?
        b := s.spawn(worker, 1000u64)?
        c := s.spawn(worker, 1000u64)?
        d := s.spawn(worker, 1000u64)?
        sum = a.get() + b.get() + c.get() + d.get()
    }
    return Ok(sum)
}
func main() -> i32 {
    match run() {
        Ok(n) => { }
        Err(e) => { }
    }
    return 0i32
}
`

func TestAllocReportThreads(t *testing.T) {
	exe := buildProgram(t, reportThreadsProgram, native.Options{})
	stderr, status := runReport(t, exe, "KERN_ALLOC_REPORT=1")
	if status != 0 {
		t.Fatalf("status %d, stderr %q", status, stderr)
	}
	r := parseReport(t, stderr).Alloc
	// the scope, and per thread a join handle, a thread, 1000 blocks freed
	// and 10 kept
	const total = 1 + 4*(1+1+1000+10)
	if r.Total != total || r.Live != 40 || r.LiveBytes != 40*32 {
		t.Errorf("total %d live %d live_bytes %d, want %d 40 1280", r.Total, r.Live, r.LiveBytes, total)
	}
	want := []allocSite{{"t.kern", lineOf(t, reportThreadsProgram, "// KEPT"), 40, 1280}}
	if fmt.Sprint(r.Sites) != fmt.Sprint(want) {
		t.Errorf("sites %v, want %v", r.Sites, want)
	}
}

// A program that traps is not reported: the handler's one line is all
// that standard error holds (RE3).
func TestAllocReportNotAfterTrap(t *testing.T) {
	exe := buildProgram(t, "module t\nfunc main() -> i32 {\n    v := Vec[u64].new()\n    v.push(1u64)\n    trap()\n    return 0i32\n}\n", native.Options{})
	stderr, status := runReport(t, exe, "KERN_ALLOC_REPORT=1")
	if status == 0 || strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, `{"trap":`) {
		t.Errorf("status %d, standard error %q: want a failure and the trap record alone", status, stderr)
	}
}
