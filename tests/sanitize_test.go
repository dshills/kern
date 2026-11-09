package tests

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
)

// sanitizedKnown lists the fixtures whose sanitized run is known to report
// something a later requirement of specs/runtime/SPEC.md owns, by fixture
// path suffix, with that requirement. RA5 is complete (gate 8) only when
// the list is empty.
var sanitizedKnown = map[string]string{}

// sanitizerReports are the markers of an AddressSanitizer, LeakSanitizer or
// UndefinedBehaviorSanitizer report on standard error.
var sanitizerReports = []string{"ERROR: AddressSanitizer", "ERROR: LeakSanitizer", "runtime error:", "AddressSanitizer:DEADLYSIGNAL"}

// TestFixturesSanitized is specs/runtime RA5: every tests/run and
// tests/concurrency fixture, built natively at debug with AddressSanitizer
// and UndefinedBehaviorSanitizer in the program and the runtime, produces
// its expected output and no sanitizer report. It runs when KERN_SANITIZE
// is set (a CI job sets it on Linux, where the leak checker runs too).
//
// A fixture that expects a trap runs only when the trap comes from a check
// instruction; `stack` and `unreachable` traps are raised through a fault
// the sanitizer intercepts, and are left out.
func TestFixturesSanitized(t *testing.T) {
	if os.Getenv("KERN_SANITIZE") == "" {
		t.Skip("set KERN_SANITIZE=1 to run the fixtures under AddressSanitizer and UndefinedBehaviorSanitizer")
	}
	if !native.Available() {
		t.Fatal("KERN_SANITIZE is set but clang is not available")
	}
	leaks := "0" // macOS's AddressSanitizer has no leak checker
	if runtime.GOOS == "linux" {
		leaks = "1"
	}
	ran := 0
	for _, path := range fixtures(t) {
		rel := filepath.ToSlash(path)
		if !strings.HasPrefix(rel, "run/") && !strings.HasPrefix(rel, "concurrency/") {
			continue
		}
		f, all := loadFixture(t, path)
		h := parseHeader(t, f)
		if h.phase > Phase || h.env == "freestanding" || h.cfile != "" || h.tsan == "race" {
			continue
		}
		switch h.expect {
		case "accept", "output":
		case "trap":
			if h.code == "stack" || h.code == "unreachable" {
				continue
			}
		default:
			continue
		}
		ran++
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			if why, known := sanitizedKnown[rel]; known {
				t.Skipf("known report, owned by %s", why)
			}
			exe := filepath.Join(t.TempDir(), "prog")
			ds, err := native.Build(all, exe, native.Options{Sanitize: true, TestHooks: h.testHooks})
			if err != nil {
				t.Fatalf("sanitized build: %v", err)
			}
			if len(ds) > 0 {
				if _, err := os.Stat(exe); err != nil {
					t.Skipf("the program has no entry to run: %s", render(ds))
				}
			}
			if _, err := os.Stat(exe); err != nil {
				return // a fixture without main: nothing was linked
			}
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe)
			cmd.Dir = t.TempDir() // fixtures that write files write them in the working directory
			cmd.Env = append(os.Environ(), "ASAN_OPTIONS=detect_leaks="+leaks+":exitcode=99:allocator_may_return_null=1", "UBSAN_OPTIONS=print_stacktrace=1")
			var out, errb bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errb
			runErr := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("the sanitized program did not finish within the budget")
			}
			for _, marker := range sanitizerReports {
				if strings.Contains(errb.String(), marker) {
					t.Fatalf("sanitizer report:\n%s", errb.String())
				}
			}
			aborted := false
			var ee *exec.ExitError
			if errors.As(runErr, &ee) {
				if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() && ws.Signal() == syscall.SIGABRT {
					aborted = true
				}
				if ee.ExitCode() == 99 {
					t.Fatalf("the sanitizer ended the program:\n%s", errb.String())
				}
			}
			if h.expect == "trap" {
				if !aborted || (!h.noRecord && !strings.Contains(errb.String(), `"kind":"`+h.code+`"`)) {
					t.Fatalf("expected a %s trap, aborted=%v stderr %q", h.code, aborted, errb.String())
				}
				return
			}
			if aborted {
				t.Fatalf("unexpected abort: %s", errb.String())
			}
			name := h.code
			if name == "" {
				name = strings.TrimSuffix(filepath.Base(path), ".kern")
			}
			if want, err := os.ReadFile(expectedOutput(path, name)); err == nil && !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("output mismatch under the sanitizers:\n--- want\n%s\n--- got\n%s", want, out.String())
			}
		})
	}
	if ran < 100 {
		t.Fatalf("only %d fixtures selected for the sanitized run", ran)
	}
}

// TestSanitizerDetects proves the sanitized build is instrumented: a
// program that reads freed memory through raw pointers, which no kern
// check catches natively, must end in an AddressSanitizer report. Without
// it a build that silently lost its instrumentation would pass RA5.
func TestSanitizerDetects(t *testing.T) {
	if os.Getenv("KERN_SANITIZE") == "" {
		t.Skip("set KERN_SANITIZE=1 to run the sanitizer tests")
	}
	if !native.Available() {
		t.Fatal("KERN_SANITIZE is set but clang is not available")
	}
	const src = `module t
contract { requires "deliberately reads freed memory" }
func stale() -> u64 {
    unsafe {
        p := ptr_cast[u64, u8](heap_alloc(64usize, 8usize))
        ptr_store(p, 7u64)
        heap_free(ptr_cast[u8, u64](p), 64usize, 8usize)
        return ptr_load(p)
    }
}
func main() -> i32 {
    print_u64(stale())
    println()
    return 0i32
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "stale.kern")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, all := loadFixture(t, file)
	exe := filepath.Join(dir, "prog")
	if ds, err := native.Build(all, exe, native.Options{Sanitize: true}); err != nil || len(ds) > 0 {
		t.Fatalf("sanitized build: %v %s", err, render(ds))
	}
	var errb bytes.Buffer
	cmd := exec.Command(exe)
	cmd.Stderr = &errb
	_ = cmd.Run()
	if !strings.Contains(errb.String(), "ERROR: AddressSanitizer: heap-use-after-free") {
		t.Fatalf("a read of freed memory was not reported; stderr %q", errb.String())
	}
}
