package freestanding

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The C reader counts external definitions only, in the runtime's style.
func TestCDefinitions(t *testing.T) {
	src := `#include <stdint.h>
typedef struct { int x; } kern_t;
static void *volatile kern_hidden = 0;
void *kern_limit = 0;
uint32_t kern_counter;
uint8_t kern_table[16];
extern int kern_elsewhere;
void kern_proto(int32_t code);
static void kern_helper(char c) {
}
void kern_one(void) {}
uint32_t kern_two(int x)
{
    return 0;
}
    void kern_indented(void) {}
// void kern_comment(void) {}
#define KERN_MACRO(x) kern_macro(x)
`
	got := strings.Join(keys(cDefinitions(src)), " ")
	if want := "kern_counter kern_limit kern_one kern_table kern_two"; got != want {
		t.Errorf("cDefinitions = %q, want %q", got, want)
	}
}

// The assembly reader counts a name both made global and defined.
func TestAsmGlobals(t *testing.T) {
	src := `    .section .text
    .globl _start
_start:
    .global defined_too
defined_too:
    .globl declared_only
local_label:
`
	if got, want := strings.Join(keys(asmGlobals(src)), " "), "_start defined_too"; got != want {
		t.Errorf("asmGlobals = %q, want %q", got, want)
	}
}

// Every runtime symbol ADR-011's table names is provided
// (specs/freestanding-availability R3).
func TestProvidedCoversADR011(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "decisions", "ADR-011-freestanding-runtime-facilities.md"))
	if err != nil {
		t.Fatal(err)
	}
	p := Provided()
	sym := regexp.MustCompile("`(kern_[a-z0-9_]+|_start)`")
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "| `") {
			continue
		}
		cell := l[:strings.Index(l[1:], "|")+1] // the table's first column
		for _, m := range sym.FindAllStringSubmatch(cell, -1) {
			n++
			if !p[m[1]] {
				t.Errorf("ADR-011 names %s, which Provided does not have", m[1])
			}
		}
	}
	if n < 10 {
		t.Fatalf("read only %d symbols from ADR-011's table", n)
	}
}

// Provided equals the defined external symbols of the compiled runtime
// and startup objects for both freestanding targets, where clang and
// llvm-nm exist (specs/freestanding-availability R3).
func TestProvidedMatchesObjects(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not on PATH")
	}
	nm, err := exec.LookPath("llvm-nm")
	if err != nil {
		t.Skip("llvm-nm not on PATH")
	}
	dir := t.TempDir()
	files := map[string]string{"rt.c": RuntimeC, "kern_rt_fs.h": Header, "f64text.c": F64TextC, "start_aarch64.S": StartAArch64, "start_x86_64.S": StartX8664}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for _, tc := range []struct{ triple, start string }{{"aarch64-unknown-none-elf", "start_aarch64.S"}, {"x86_64-unknown-none-elf", "start_x86_64.S"}} {
		for _, src := range []string{"rt.c", "f64text.c", tc.start} {
			obj := filepath.Join(dir, tc.triple+"-"+src+".o")
			args := []string{"-target", tc.triple, "-ffreestanding", "-nostdlib", "-fno-builtin", "-O1", "-c", "-o", obj, filepath.Join(dir, src)}
			if out, err := exec.Command(clang, args...).CombinedOutput(); err != nil {
				t.Fatalf("clang %s %s: %v\n%s", tc.triple, src, err, out)
			}
			out, err := exec.Command(nm, "--defined-only", "--extern-only", "--format=just-symbols", obj).Output()
			if err != nil {
				t.Fatalf("llvm-nm %s: %v", obj, err)
			}
			for _, s := range strings.Fields(string(out)) {
				got[s] = true
			}
		}
	}
	if g, w := strings.Join(keys(got), " "), strings.Join(keys(Provided()), " "); g != w {
		t.Errorf("objects define\n  %s\nProvided has\n  %s", g, w)
	}
}
