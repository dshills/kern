package tests

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
)

// TestFixtures builds two executables for every fixture that runs, and the
// builds are most of its time: one clang after another on one processor.
// They are made here before the fixture loop starts, several at once, and
// each subtest then takes its own. Only the builds are concurrent: every
// check, every interpreter run and every program still runs in the loop,
// one at a time and in the same order, after the last build has finished
// (docs/reports/perf-review.md 6.3).

// maxTestJobs bounds the default of testJobs: past it more processes buy
// little, and the packages `go test ./...` runs beside this one are
// starved.
const maxTestJobs = 16

// testJobs is how many builds or programs a test of this package starts at
// once where it keeps a pool of them: KERN_TEST_JOBS when it is set (1 for
// a loaded host or a small CI runner), otherwise twice the processors Go
// may use (a job is mostly a child process, which starts, reads and writes
// files as well as computing) and at most maxTestJobs. 224 builds on a
// loaded machine of 14 processors: 59 s at 1, 9.1 to 10.1 s at 7, 8.7 to
// 9.9 s at 14, 7.3 to 7.6 s at 28.
func testJobs(t *testing.T) int {
	t.Helper()
	if v := os.Getenv("KERN_TEST_JOBS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("KERN_TEST_JOBS=%q: want a positive integer", v)
		}
		return n
	}
	return min(2*runtime.GOMAXPROCS(0), maxTestJobs)
}

// aheadBuild is one native build of a fixture made before the fixture
// loop. A worker writes the results; the fixture's subtest reads them once
// every worker has finished.
type aheadBuild struct {
	path  string // the fixture
	level string // debug | release
	exe   string
	done  bool // native.Build returned: ds and err are its results
	ds    []diag.Diagnostic
	err   error
	// a panic in the build, and its stack: the fixture's subtest raises it
	panicked any
	stack    []byte
}

// run loads the fixture's files, a copy of its own so that no build shares
// one with another, and builds them as nativeRunOpt does.
func (b *aheadBuild) run(h header) {
	defer func() {
		if v := recover(); v != nil {
			b.panicked, b.stack = v, debug.Stack()
		}
	}()
	_, files, err := readFixture(b.path)
	if err != nil {
		return // the subtest finds the same and reports it
	}
	if err := os.MkdirAll(filepath.Dir(b.exe), 0o755); err != nil {
		return // the subtest builds in its own directory
	}
	b.ds, b.err = native.Build(files, b.exe, nativeOptions(h, b.level))
	b.done = true
}

// aheadBuilds are the builds made ahead for one run of TestFixtures.
type aheadBuilds struct {
	builds map[[2]string]*aheadBuild // by fixture path and level
	late   int                       // builds a subtest had to make itself
}

// take is the build made ahead for a fixture at a level, once: nil when
// there is none (no build was made ahead, or this one could not start),
// and the subtest then builds as it always did.
func (a *aheadBuilds) take(t *testing.T, path, level string) *aheadBuild {
	t.Helper()
	if a == nil {
		return nil
	}
	b := a.builds[[2]string{path, level}]
	if b == nil || (!b.done && b.panicked == nil) {
		a.late++
		return nil
	}
	delete(a.builds, [2]string{path, level})
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(b.exe)) })
	if b.panicked != nil {
		panic(fmt.Sprintf("native build of %s (%s): panic: %v\n%s", b.path, b.level, b.panicked, b.stack))
	}
	return b
}

// comparesNatively reports whether TestFixtures runs the fixture natively
// beside the interpreter (execute reaches nativeRun), as far as its header
// says: an accepted fixture of a phase that executes, hosted, executable
// by the interpreter and not a deliberate race. One that turns out to have
// no main function, or diagnostics, costs a build that stops before clang
// and that no subtest asks for (238 of the 928 builds when this was
// written, 10 to 20 ms of one processor each).
func comparesNatively(h header) bool {
	switch h.expect {
	case "accept", "output", "trap":
	default:
		return false
	}
	return h.phase >= 1 && h.phase <= Phase && h.cfile == "" && !h.native && h.env != "freestanding" && h.tsan != "race"
}

// buildAhead builds, on testJobs workers, the executables nativeRun will
// ask for: for each fixture `go test` will run (-run and -skip) that
// comparesNatively, one for each of nativeLevels. It returns when every
// build has finished. A fixture that cannot be read or has a bad header is
// left to its subtest, which reports it.
//
// Concurrent builds in one process are safe: native.Build keeps no state
// between calls but the runtime object cache, which is written for
// concurrent builds (objects named by content, the manifest published by a
// rename), and the compiler packages it calls have no package state but
// read-only tables and compiler/layout's sync.Map; the differential tests
// of this package already build from parallel subtests.
func buildAhead(t *testing.T, paths []string) *aheadBuilds {
	t.Helper()
	workers := testJobs(t)
	if !native.Available() {
		return nil
	}
	filter := testFilter()
	root := t.TempDir()
	a := &aheadBuilds{builds: map[[2]string]*aheadBuild{}}
	type job struct {
		b *aheadBuild
		h header
	}
	var jobs []job
	for i, path := range paths {
		if !filter.selects(t.Name() + "/" + path) {
			continue
		}
		f, _, err := readFixture(path)
		if err != nil {
			continue
		}
		h, err := readHeader(f)
		if err != nil || !comparesNatively(h) {
			continue
		}
		for _, level := range nativeLevels(h) {
			b := &aheadBuild{path: path, level: level, exe: filepath.Join(root, fmt.Sprintf("%04d-%s", i, level), "prog")}
			a.builds[[2]string{path, level}] = b
			jobs = append(jobs, job{b, h})
		}
	}
	if len(jobs) == 0 {
		return a
	}
	workers = min(workers, len(jobs))
	start := time.Now()
	queue := make(chan job)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range queue {
				j.b.run(j.h)
			}
		})
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()
	t.Logf("%d native builds made ahead, %d at a time, in %s", len(jobs), workers, time.Since(start).Round(time.Millisecond))
	return a
}

// nameFilter is the testing package's reading of -run and -skip
// (testing/match.go): a pattern is alternatives separated by `|`, each a
// sequence of regular expressions separated by `/`, one for each element
// of a test's name.
type nameFilter struct {
	run, skip [][]*regexp.Regexp
}

// testFilter is the filter of this test run's flags.
func testFilter() nameFilter {
	value := func(name string) string {
		if f := flag.Lookup(name); f != nil {
			return f.Value.String()
		}
		return ""
	}
	return newNameFilter(value("test.run"), value("test.skip"))
}

func newNameFilter(run, skip string) nameFilter {
	var f nameFilter
	if run != "" {
		f.run = splitPattern(run)
	}
	if skip != "" {
		f.skip = splitPattern(skip)
	}
	return f
}

// selects reports whether a subtest of the given full name runs. It need
// not be exact: a fixture it wrongly leaves out is built by its subtest,
// and one it wrongly keeps is built for nothing.
func (f nameFilter) selects(name string) bool {
	elems := strings.Split(spacesToUnderscores(name), "/")
	if f.run != nil {
		if ok, _ := matchAlternatives(f.run, elems); !ok {
			return false
		}
	}
	ok, partial := matchAlternatives(f.skip, elems)
	return !ok || partial
}

// matchAlternatives is the first alternative every element of which
// matches the name's element at its place; partial says the name is
// shorter than that alternative.
func matchAlternatives(alts [][]*regexp.Regexp, elems []string) (ok, partial bool) {
next:
	for _, alt := range alts {
		for i, s := range elems {
			if i >= len(alt) {
				break
			}
			if alt[i] == nil || !alt[i].MatchString(s) {
				continue next
			}
		}
		return true, len(elems) < len(alt)
	}
	return false, false
}

// splitPattern splits a -run or -skip pattern as testing's splitRegexp
// does: at `|` and `/` outside brackets and parentheses. An element that
// is not a regular expression matches nothing (the testing package has
// refused it before any test runs).
func splitPattern(s string) [][]*regexp.Regexp {
	var alts [][]*regexp.Regexp
	var alt []*regexp.Regexp
	element := func(e string) {
		re, err := regexp.Compile(spacesToUnderscores(e))
		if err != nil {
			re = nil
		}
		alt = append(alt, re)
	}
	squares, parens := 0, 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '[':
			squares++
		case ']':
			if squares--; squares < 0 { // an unmatched ']' is legal
				squares = 0
			}
		case '(':
			if squares == 0 {
				parens++
			}
		case ')':
			if squares == 0 {
				parens--
			}
		case '\\':
			i++
		case '/', '|':
			if squares == 0 && parens == 0 {
				element(s[:i])
				if s[i] == '|' {
					alts = append(alts, alt)
					alt = nil
				}
				s = s[i+1:]
				i = 0
				continue
			}
		}
		i++
	}
	element(s)
	return append(alts, alt)
}

// spacesToUnderscores is the part of testing's rewrite of a subtest name
// that a fixture path can meet.
func spacesToUnderscores(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r':
			return '_'
		}
		return r
	}, s)
}

// TestNameFilter checks nameFilter against what the testing package does
// with the same flags.
func TestNameFilter(t *testing.T) {
	for _, c := range []struct {
		run, skip, name string
		want            bool
	}{
		{"", "", "TestFixtures/run/hello.kern", true},
		{"TestFixtures$", "", "TestFixtures/run/hello.kern", true},
		{"TestFixtures$/run/hello", "", "TestFixtures/run/hello.kern", true},
		{"TestFixtures$/run/hello", "", "TestFixtures/run/hello_world.kern", true},
		{"TestFixtures$/run/^hello.kern$", "", "TestFixtures/run/hello_world.kern", false},
		{"TestFixtures$/run/hello", "", "TestFixtures/trap/hello.kern", false},
		{"TestFixtures$/run", "", "TestFixtures/concurrency/litmus/sb.kern", false},
		{"TestFixtures$/run", "", "TestFixtures/run/dir/deeper", true},
		{"TestFixtures$/run/a/b/c", "", "TestFixtures/run/a", true},
		{"TestFixtures$/[/]|x/hello", "", "TestFixtures/run/hello.kern", false},
		{"TestOther|TestFixtures/trap", "", "TestFixtures/trap/bounds.kern", true},
		{"TestOther|TestFixtures/trap", "", "TestFixtures/run/hello.kern", false},
		{"TestFixtures/(run|trap)/hello", "", "TestFixtures/trap/hello.kern", true},
		{"TestFixtures/(run|trap)/hello", "", "TestFixtures/ffi/hello.kern", false},
		{`TestFixtures/\.\./examples`, "", "TestFixtures/../examples/kq/main.kern", true},
		{"", "TestFixtures/run", "TestFixtures/run/hello.kern", false},
		{"", "TestFixtures/run", "TestFixtures/trap/bounds.kern", true},
		{"", "TestFixtures/run/hello/more", "TestFixtures/run/hello.kern", true},
		{"", "TestOther|TestFixtures/run/hello", "TestFixtures/run/hello.kern", false},
		{"TestFixtures/run", "hello", "TestFixtures/run/hello.kern", true},
		{"TestFixtures/run", "Fixtures", "TestFixtures/run/hello.kern", false},
		{"TestFixtures/a b", "", "TestFixtures/a_b.kern", true},
	} {
		if got := newNameFilter(c.run, c.skip).selects(c.name); got != c.want {
			t.Errorf("-run %q -skip %q: %s selected %v, want %v", c.run, c.skip, c.name, got, c.want)
		}
	}
}
