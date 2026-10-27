// Package native builds kern programs to native executables: the semantic IR
// is emitted as textual LLVM IR and compiled with clang together with the
// hosted runtime (PLAN.md 2.1, 2.2).
package native

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dshills/kern/compiler/backend/llvm"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
	"github.com/dshills/kern/runtime/freestanding"
	"github.com/dshills/kern/runtime/hosted"
)

// Options selects the build configuration (PLAN.md §0).
type Options struct {
	Opt      string // debug | release
	Triple   string // LLVM target triple; "" is the host
	EmitLLVM string // when set, the LLVM IR text is also written here
	// ExtraSources are C files compiled and linked with the program (FFI
	// fixtures' counterparts).
	ExtraSources []string
	// TestHooks links the test-only runtime symbols (`testing.*`); without
	// it a program referencing them fails to link (PLAN.md 2.3).
	TestHooks bool
	// Freestanding builds for a bare-metal target with the freestanding
	// runtime (PLAN.md 3.1): no OS, no allocator, prologue stack checks.
	Freestanding bool
	// TSan instruments the program and runtime with ThreadSanitizer
	// (concurrency fixtures, PLAN.md 3.3).
	TSan bool
	// ElideChecks is the measurement-only option that drops runtime checks
	// (PLAN.md 4.4); it is not a build configuration of the language.
	ElideChecks bool
	// Debug emits DWARF line information for the debugger (`kern debug`).
	Debug bool
	// NoStackCheck is the unsafe build option that drops the freestanding
	// prologue check (stack overflow becomes a trusted-boundary obligation).
	NoStackCheck bool
}

// FreestandingTriple is the default bare-metal target (QEMU virt).
const FreestandingTriple = "aarch64-unknown-none-elf"

// FreestandingTripleX8664 is the x86-64 bare-metal target (QEMU q35).
const FreestandingTripleX8664 = "x86_64-unknown-none-elf"

// Linkable reports whether freestanding images can be linked here (lld).
func Linkable() bool {
	_, err := exec.LookPath("ld.lld")
	return err == nil
}

// QEMUFor is the QEMU system emulator and machine arguments for a
// freestanding triple: the virt machine for AArch64, q35 with the
// isa-debug-exit device for x86-64 (PLAN.md 3.1). The second result is nil
// for any other triple.
func QEMUFor(triple string) (string, []string) {
	switch {
	case strings.HasPrefix(triple, "aarch64"):
		return "qemu-system-aarch64", []string{"-M", "virt", "-cpu", "cortex-a72", "-serial", "stdio", "-display", "none", "-no-reboot"}
	case strings.HasPrefix(triple, "x86_64"):
		return "qemu-system-x86_64", []string{"-M", "q35", "-serial", "stdio", "-display", "none", "-no-reboot", "-device", "isa-debug-exit,iobase=0xf4,iosize=0x04"}
	}
	return "", nil
}

// QEMUAvailable reports whether the system emulator for the triple is
// installed.
func QEMUAvailable(triple string) bool {
	emu, _ := QEMUFor(triple)
	if emu == "" {
		return false
	}
	_, err := exec.LookPath(emu)
	return err == nil
}

// HostTriple is the LLVM target triple of the build host.
func HostTriple() string {
	arch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	switch runtime.GOOS {
	case "darwin":
		return arch + "-apple-macosx"
	default:
		return arch + "-unknown-linux-gnu"
	}
}

// ErrNoClang reports that no clang is on the PATH.
var ErrNoClang = errors.New("clang not found")

// Available reports whether a native build can run on this machine.
func Available() bool {
	_, err := exec.LookPath("clang")
	return err == nil
}

// Build compiles srcs to the executable at out. Diagnostics are the user's
// errors; the error is a toolchain or backend failure.
func Build(srcs []*source.File, out string, opts Options) ([]diag.Diagnostic, error) {
	prog := resolve.Resolve(srcs)
	if len(prog.Diags) > 0 {
		return prog.Diags, nil
	}
	if ds := check.Check(prog); len(ds) > 0 {
		return ds, nil
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		return ds, nil
	}
	if errs := ir.Validate(mod); len(errs) > 0 {
		return nil, fmt.Errorf("ir: %v", errs[0])
	}
	if mod.Entry == "" {
		return nil, errors.New("no main function")
	}
	triple := opts.Triple
	if triple == "" {
		triple = HostTriple()
		if opts.Freestanding {
			triple = FreestandingTriple
		}
	}
	lo := llvm.Options{Triple: triple, Freestanding: opts.Freestanding, StackCheck: opts.Freestanding && !opts.NoStackCheck, TSan: opts.TSan, ElideChecks: opts.ElideChecks}
	if opts.Debug {
		lo.Sources = map[string][]byte{}
		for _, f := range srcs {
			lo.Sources[f.Name] = f.Bytes
		}
		lo.Sources["std/core.kern"] = resolve.PreludeSource()
	}
	text, errs := llvm.EmitWith(mod, lo)
	if len(errs) > 0 {
		return nil, fmt.Errorf("backend: %v", errs[0])
	}
	text = withProgramName(text, srcs[0].Name, opts.Freestanding)
	if opts.EmitLLVM != "" {
		if err := os.WriteFile(opts.EmitLLVM, []byte(text), 0o644); err != nil {
			return nil, err
		}
	}
	if opts.Freestanding {
		return nil, buildFreestanding(text, out, triple, opts.Opt == "release")
	}
	clang, err := exec.LookPath("clang")
	if err != nil {
		return nil, ErrNoClang
	}
	dir, err := os.MkdirTemp("", "kern-build-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ll := filepath.Join(dir, "prog.ll")
	rt := filepath.Join(dir, "kern_rt.c")
	if err := os.WriteFile(ll, []byte(text), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rt, []byte(hosted.Source), 0o644); err != nil {
		return nil, err
	}
	level := "-O1"
	if opts.Opt == "release" {
		level = "-O2"
	}
	args := []string{level, "-Wno-override-module", "-ffp-model=strict", "-o", out, ll, rt}
	if opts.Opt == "release" {
		args = append(args, "-DKERN_RELEASE") // identity tags off (PLAN.md 2.2)
	}
	if opts.TestHooks {
		args = append(args, "-DKERN_TEST_HOOKS")
	}
	if opts.TSan {
		args = append(args, "-fsanitize=thread", "-g")
	}
	if opts.Debug {
		args = append(args, "-g", "-O0")
	}
	args = append(args, opts.ExtraSources...)
	args = append(args, "-lpthread")
	if triple != HostTriple() {
		args = append(args, "-target", triple)
	}
	cmd := exec.Command(clang, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if len(msg) > 4096 {
			msg = msg[:4096]
		}
		return nil, fmt.Errorf("clang failed: %v: %s", err, msg)
	}
	return nil, nil
}

// EmitAsm compiles srcs to target assembly text (the emit-asm command).
func EmitAsm(srcs []*source.File, triple string) (string, []diag.Diagnostic, error) {
	prog := resolve.Resolve(srcs)
	if len(prog.Diags) > 0 {
		return "", prog.Diags, nil
	}
	if ds := check.Check(prog); len(ds) > 0 {
		return "", ds, nil
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		return "", ds, nil
	}
	if errs := ir.Validate(mod); len(errs) > 0 {
		return "", nil, fmt.Errorf("ir: %v", errs[0])
	}
	if triple == "" {
		triple = HostTriple()
	}
	text, errs := llvm.Emit(mod, triple)
	if len(errs) > 0 {
		return "", nil, fmt.Errorf("backend: %v", errs[0])
	}
	if mod.Entry != "" {
		text = withProgramName(text, srcs[0].Name, false)
	}
	clang, err := exec.LookPath("clang")
	if err != nil {
		return "", nil, ErrNoClang
	}
	dir, err := os.MkdirTemp("", "kern-asm-")
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ll := filepath.Join(dir, "prog.ll")
	asm := filepath.Join(dir, "prog.s")
	if err := os.WriteFile(ll, []byte(text), 0o644); err != nil {
		return "", nil, err
	}
	args := []string{"-O1", "-Wno-override-module", "-ffp-model=strict", "-S", "-o", asm, ll}
	if triple != HostTriple() {
		args = append(args, "-target", triple)
	}
	cmd := exec.Command(clang, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("clang failed: %v: %s", err, stderr.String())
	}
	b, err := os.ReadFile(asm)
	if err != nil {
		return "", nil, err
	}
	return string(b), nil, nil
}

// withProgramName embeds the program's file name for the trap record.
func withProgramName(text, name string, freestanding bool) string {
	esc := strings.NewReplacer("\\", "\\5C", "\"", "\\22").Replace(name)
	global := fmt.Sprintf("@kern.progname = private constant [%d x i8] c\"%s\\00\"\n", len(name)+1, esc)
	entry := "define i32 @main() {\nentry:\n"
	if freestanding {
		entry = "define i32 @kern_main() {\nentry:\n"
	}
	text = strings.Replace(text, "declare void @kern_start()", "declare void @kern_start()\ndeclare void @kern_set_file(ptr)", 1)
	return strings.Replace(text, entry, global+entry+"  call void @kern_set_file(ptr @kern.progname)\n", 1)
}

// buildFreestanding compiles the program, the freestanding runtime and the
// startup code for a bare-metal triple. With ld.lld on the PATH the result
// is a linked image: an ELF laid out for QEMU's virt machine on AArch64, and
// on x86-64 a Multiboot 1 image for q35 laid out by q35.ld and rewritten to
// the 32-bit ELF class with llvm-objcopy, since QEMU's Multiboot loader
// rejects a 64-bit ELF. Without ld.lld the result is an archive of the
// three objects, which the user links with the script shipped in
// runtime/freestanding/src.
func buildFreestanding(text, out, triple string, release bool) error {
	clang, err := exec.LookPath("clang")
	if err != nil {
		return ErrNoClang
	}
	dir, err := os.MkdirTemp("", "kern-fs-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	start, script, x86 := freestanding.StartAArch64, freestanding.VirtLinkerScript, false
	if strings.HasPrefix(triple, "x86_64") {
		start, script, x86 = freestanding.StartX8664, freestanding.Q35LinkerScript, true
	}
	files := map[string]string{"prog.ll": text, "rt.c": freestanding.RuntimeC, "start.S": start, "image.ld": script}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	common := []string{"-target", triple, "-ffreestanding", "-nostdlib", "-fno-builtin", "-O1", "-Wno-override-module", "-ffp-model=strict", "-c"}
	if release {
		common = append(common, "-DKERN_RELEASE") // identity tags off (PLAN.md 2.2), as in a hosted release build
	}
	if !x86 {
		// the image runs with the MMU off, where every access is to Device
		// memory and must be aligned to its size: no unaligned (vector-widened)
		// loads and stores
		common = append(common, "-mstrict-align")
	}
	var objs []string
	for _, src := range []string{"prog.ll", "rt.c", "start.S"} {
		obj := filepath.Join(dir, src+".o")
		cmd := exec.Command(clang, append(append([]string{}, common...), "-o", obj, filepath.Join(dir, src))...)
		if msg, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("clang %s: %v: %s", src, err, msg)
		}
		objs = append(objs, obj)
	}
	if lld, err := exec.LookPath("ld.lld"); err == nil {
		args := append([]string{"-nostdlib", "-static", "-T", filepath.Join(dir, "image.ld"), "-o", out}, objs...)
		if msg, err := exec.Command(lld, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("ld.lld: %v: %s", err, msg)
		}
		if !x86 {
			return nil
		}
		objcopy, err := exec.LookPath("llvm-objcopy")
		if err != nil {
			// clang's own toolchain directory, when the PATH lacks it
			objcopy = filepath.Join(filepath.Dir(clang), "llvm-objcopy")
			if _, err := os.Stat(objcopy); err != nil {
				return fmt.Errorf("llvm-objcopy not found: an x86-64 Multiboot image needs the 32-bit ELF class")
			}
		}
		if msg, err := exec.Command(objcopy, "-O", "elf32-i386", out).CombinedOutput(); err != nil {
			return fmt.Errorf("llvm-objcopy: %v: %s", err, msg)
		}
		return nil
	}
	ar, err := exec.LookPath("llvm-ar")
	if err != nil {
		ar = "ar"
	}
	_ = os.Remove(out)
	if msg, err := exec.Command(ar, append([]string{"rcs", out}, objs...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("ar: %v: %s", err, msg)
	}
	return nil
}
