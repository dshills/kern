package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/source"
	"github.com/dshills/kern/runtime/freestanding"
)

// The freestanding board interface (specs/runtime RJ1): a board file of
// the user's, a linker script of the user's and the heap's size, each
// built into an image that boots under QEMU. The fixtures cover the
// default boards; these need build options a fixture cannot give.

var fsTriples = []string{native.FreestandingTriple, native.FreestandingTripleX8664}

// buildImage builds a freestanding image of src.
func buildImage(t *testing.T, src string, opts native.Options) (string, error) {
	t.Helper()
	if !native.Available() {
		t.Skip("clang not available")
	}
	opts.Freestanding = true
	img := filepath.Join(t.TempDir(), "image")
	f := &source.File{Name: "fs.kern", Bytes: []byte(src)}
	ds, err := native.Build([]*source.File{f}, img, opts)
	if len(ds) > 0 {
		t.Fatalf("diagnostics: %s", render(ds))
	}
	return img, err
}

// bootable skips a test that needs to boot an image where it cannot.
func bootable(t *testing.T, triple string) {
	t.Helper()
	if !native.Linkable() || !native.QEMUAvailable(triple) {
		if os.Getenv("KERN_REQUIRE_QEMU") != "" {
			t.Fatalf("ld.lld or QEMU for %s is required and missing", triple)
		}
		t.Skipf("ld.lld or QEMU for %s not available", triple)
	}
}

const boardProgram = `module fs
import "std/time"
func main() -> i32 {
    print_str("hello from a board of the program's own")
    println()
    time.sleep(time.seconds(3600u64))
    print_str("an hour's sleep returns at once without a clock; the time is ")
    print_u64(now_ns())
    println()
    return 7i32
}
`

// A board file of the test's own: its set-up writes a line, its console
// prefixes every line, it has no clock, and it passes the halt code on.
func TestCustomBoard(t *testing.T) {
	want := []byte("up\n| hello from a board of the program's own\n| an hour's sleep returns at once without a clock; the time is 0\n| {\"halt\":{\"code\":7}}\n")
	for _, triple := range fsTriples {
		t.Run(triple, func(t *testing.T) {
			bootable(t, triple)
			img, err := buildImage(t, boardProgram, native.Options{Triple: triple, Board: filepath.Join("board", "prefix_board.c")})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			qemuRunCode(t, triple, img, want, 7)
		})
	}
}

// A board file must define all four functions: one that leaves the clock
// out does not link, and the message names what is missing.
func TestBoardMustDefineAllFour(t *testing.T) {
	if !native.Linkable() {
		t.Skip("ld.lld not available: nothing is linked")
	}
	_, err := buildImage(t, boardProgram, native.Options{Triple: native.FreestandingTriple, Board: filepath.Join("board", "no_clock_board.c")})
	if err == nil || !strings.Contains(err.Error(), "kern_board_now_ns") {
		t.Errorf("build with a board that has no clock: %v, want a link error naming kern_board_now_ns", err)
	}
	_, err = buildImage(t, boardProgram, native.Options{Triple: native.FreestandingTriple, Board: filepath.Join("board", "no_such_board.c")})
	if err == nil || !strings.Contains(err.Error(), "board file") {
		t.Errorf("build with a board file that does not exist: %v", err)
	}
}

const heapProgram = `module fs
contract { requires "the blocks are the heap's whole content and are freed once each" }
func main() -> i32 {
    a := SystemAllocator.new()
    var blocks: [512]usize = zero()
    var n: usize = 0usize
    unsafe {
        while n < 512usize {
            match a.alloc(4096usize, 16usize) {
                Ok(p) => { blocks[n] = ptr_addr(p) }
                Err(e) => { break }
            }
            n = n + 1usize
        }
    }
    print_str("blocks of 4096 bytes before OutOfMemory: ")
    print_u64(u64(n))
    println()
    unsafe {
        for i in 0usize..n { a.free(ptr_from_addr[u8](blocks[i]), 4096usize, 16usize) }
        match a.alloc(n * 4096usize, 16usize) {
            Ok(p) => {
                print_str("freed, the heap is one block again")
                println()
                a.free(p, n * 4096usize, 16usize)
            }
            Err(e) => { print_str("the freed blocks did not join"); println() }
        }
    }
    return 0i32
}
`

// The heap's size is what --heap-bytes says: 64 KiB hold sixteen blocks
// of 4,096 bytes and the default megabyte 256, and running out is
// Err(OutOfMemory), not a trap.
func TestHeapBytes(t *testing.T) {
	for _, tc := range []struct {
		heap   uint64
		blocks string
	}{{65536, "16"}, {0, "256"}} {
		want := []byte("blocks of 4096 bytes before OutOfMemory: " + tc.blocks + "\nfreed, the heap is one block again\n{\"halt\":{\"code\":0}}\n")
		for _, triple := range fsTriples {
			t.Run(tc.blocks+" on "+triple, func(t *testing.T) {
				bootable(t, triple)
				img, err := buildImage(t, heapProgram, native.Options{Triple: triple, HeapBytes: tc.heap})
				if err != nil {
					t.Fatalf("build: %v", err)
				}
				qemuRun(t, triple, img, want)
			})
		}
	}
	if _, err := buildImage(t, heapProgram, native.Options{Triple: native.FreestandingTriple, HeapBytes: 1000}); err == nil {
		t.Errorf("a heap of 1000 bytes, not a multiple of 16, was accepted")
	}
}

// A linker script of the user's replaces the default: the same image
// laid out a megabyte higher boots as before, and its entry is where the
// script put it.
func TestLinkerScript(t *testing.T) {
	triple := native.FreestandingTriple
	bootable(t, triple)
	script := filepath.Join(t.TempDir(), "moved.ld")
	text := "ENTRY(_start)\nSECTIONS {\n  . = 0x40180000;\n  .text : { *(.text._start) *(.text*) }\n  .rodata : { *(.rodata*) }\n  .data : { *(.data*) }\n  .bss : { *(.bss*) *(COMMON) }\n}\n"
	if err := os.WriteFile(script, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "module fs\nfunc main() -> i32 {\n    print_str(\"laid out by another script\")\n    println()\n    return 0i32\n}\n"
	img, err := buildImage(t, src, native.Options{Triple: triple, LinkerScript: script})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	qemuRun(t, triple, img, []byte("laid out by another script\n{\"halt\":{\"code\":0}}\n"))
	nm, err := exec.LookPath("llvm-nm")
	if err != nil {
		t.Log("llvm-nm not on PATH: the entry's address is not checked")
		return
	}
	out, err := exec.Command(nm, img).Output()
	if err != nil {
		t.Fatalf("llvm-nm: %v", err)
	}
	if !bytes.Contains(out, []byte("0000000040180000 T _start")) {
		t.Errorf("_start is not at 0x40180000:\n%s", out)
	}
}

// The three options as flags of kern build and as keys of kern.mod, and
// the flags' refusals.
func TestBuildFreestandingOptions(t *testing.T) {
	kern := kernBinary(t)
	triple := native.FreestandingTriple
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	board, err := os.ReadFile(filepath.Join("board", "prefix_board.c"))
	if err != nil {
		t.Fatal(err)
	}
	write("board.c", string(board))
	prog := write("main.kern", heapProgram)
	want := []byte("up\n| blocks of 4096 bytes before OutOfMemory: 8\n| freed, the heap is one block again\n| {\"halt\":{\"code\":0}}\n")
	run := func(args ...string) (string, error) {
		cmd := exec.Command(kern, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Run("flags", func(t *testing.T) {
		bootable(t, triple)
		img := filepath.Join(dir, "by-flags")
		if out, err := run("build", "--env=freestanding", "--target="+triple, "--board=board.c", "--heap-bytes=32768", "-o", img, prog); err != nil {
			t.Fatalf("kern build: %v\n%s", err, out)
		}
		qemuRunCode(t, triple, img, want, 0)
	})
	t.Run("kern.mod", func(t *testing.T) {
		bootable(t, triple)
		write("kern.mod", `{"entry": "main.kern", "env": "freestanding", "target": "`+triple+`", "output": "by-manifest", "board": "board.c", "heap_bytes": 32768}`)
		defer func() { _ = os.Remove(filepath.Join(dir, "kern.mod")) }()
		if out, err := run("build"); err != nil {
			t.Fatalf("kern build: %v\n%s", err, out)
		}
		qemuRunCode(t, triple, filepath.Join(dir, "by-manifest"), want, 0)
	})
	refused := func(t *testing.T, code string, args ...string) {
		t.Helper()
		out, err := run(args...)
		var resp struct {
			OK          bool `json:"ok"`
			Diagnostics []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"diagnostics"`
		}
		if err == nil || json.Unmarshal([]byte(out), &resp) != nil || resp.OK || len(resp.Diagnostics) == 0 || resp.Diagnostics[0].Code != code {
			t.Errorf("kern %s: %v, want %s\n%s", strings.Join(args, " "), err, code, out)
		}
	}
	t.Run("refusals", func(t *testing.T) {
		img := filepath.Join(dir, "never")
		refused(t, "CLI_INVALID_FLAGS", "build", "--board=board.c", "-o", img, prog)                         // a hosted build has no board
		refused(t, "CLI_INVALID_FLAGS", "build", "--env=freestanding", "--heap-bytes=1000", "-o", img, prog) // not a multiple of 16
		refused(t, "CLI_INVALID_FLAGS", "build", "--env=freestanding", "--heap-bytes=0", "-o", img, prog)    // not positive
		refused(t, "CLI_INVALID_FLAGS", "build", "--env=freestanding", "--board=missing.c", "-o", img, prog) // no such file
		refused(t, "CLI_INVALID_FLAGS", "build", "--env=freestanding", "--linker-script=missing.ld", "-o", img, prog)
		refused(t, "CLI_INVALID_FLAGS", "run", "--heap-bytes=65536", prog) // an option of build
		refused(t, "CLI_INVALID_FLAGS", "build", "--env=freestanding", "--startup=missing.S", "-o", img, prog)
		refused(t, "CLI_INVALID_FLAGS", "build", "--startup=board.c", "-o", img, prog) // a hosted build has no start-up file
		write("kern.mod", `{"entry": "main.kern", "heap_bytes": 100}`)
		refused(t, "PROJECT_INVALID", "build", "-o", img)
		write("kern.mod", `{"entry": "main.kern", "board": "../board.c"}`)
		refused(t, "PROJECT_INVALID", "build", "-o", img)
		_ = os.Remove(filepath.Join(dir, "kern.mod"))
	})
}

// qemuConsole boots an image and returns what it wrote to its console.
func qemuConsole(t *testing.T, triple, img string) string {
	t.Helper()
	emu, args := native.QEMUFor(triple)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, emu, append(args, "-kernel", img)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run() // the x86-64 machine's exit status is the halt code's
	if ctx.Err() != nil {
		t.Fatalf("QEMU (%s) did not exit within the budget", triple)
	}
	return strings.ReplaceAll(out.String(), "\r\n", "\n")
}

// A recursion that needs some 300 KiB of stack: each activation keeps an
// array its callee reads, so no frame can be dropped.
const fsDeepProgram = `module fs
func deep(n: u64, up: view[]u64) -> u64 {
    var pad: [64]u64 = zero()
    pad[0usize] = up[0usize] + 1u64
    if n == 0u64 { return pad[0usize] }
    r := deep(n - 1u64, pad)
    return r + pad[63usize]
}
func main() -> i32 {
    var first: [64]u64 = zero()
    print_u64(deep(500u64, first))
    println()
    return 0i32
}
`

// A freestanding image's stack is --stack-bytes (specs/runtime RL5): the
// default 64 KiB ends this recursion in a stack trap, a megabyte carries
// it through.
func TestFreestandingStackBytes(t *testing.T) {
	for _, triple := range fsTriples {
		t.Run(triple, func(t *testing.T) {
			bootable(t, triple)
			img, err := buildImage(t, fsDeepProgram, native.Options{Triple: triple})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := qemuConsole(t, triple, img); !strings.Contains(got, `{"trap":{"kind":"stack"`) || !strings.HasSuffix(got, "{\"halt\":{\"code\":134}}\n") {
				t.Errorf("with the default stack the console is %q, want a stack trap", got)
			}
			img, err = buildImage(t, fsDeepProgram, native.Options{Triple: triple, StackBytes: 1 << 20})
			if err != nil {
				t.Fatalf("build with a stack of 1 MiB: %v", err)
			}
			qemuRun(t, triple, img, []byte("501\n{\"halt\":{\"code\":0}}\n"))
		})
	}
}

// --startup replaces the architecture's start-up code (RL5): this one is
// the default with one more call, which writes a byte before the program
// runs.
func TestStartupFile(t *testing.T) {
	triple := native.FreestandingTriple
	bootable(t, triple)
	const call = "    bl kern_main"
	if !strings.Contains(freestanding.StartAArch64, call) {
		t.Fatal("the default start-up code does not call kern_main as this test expects")
	}
	text := strings.Replace(freestanding.StartAArch64, call, "    mov w0, #83\n    bl kern_board_putc\n"+call, 1)
	startup := filepath.Join(t.TempDir(), "start.S")
	if err := os.WriteFile(startup, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "module fs\nfunc main() -> i32 {\n    print_str(\"tarted by other code\")\n    println()\n    return 0i32\n}\n"
	img, err := buildImage(t, src, native.Options{Triple: triple, Startup: startup})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	qemuRun(t, triple, img, []byte("Started by other code\n{\"halt\":{\"code\":0}}\n"))
	if _, err := buildImage(t, src, native.Options{Triple: triple, Startup: filepath.Join(t.TempDir(), "missing.S")}); err == nil || !strings.Contains(err.Error(), "start-up file") {
		t.Errorf("build with a start-up file that does not exist: %v", err)
	}
}
