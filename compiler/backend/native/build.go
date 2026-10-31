// Package native builds kern programs to native executables: the semantic IR
// is emitted as textual LLVM IR and compiled with clang together with the
// hosted runtime (PLAN.md 2.1, 2.2).
package native

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/dshills/kern/compiler/backend/llvm"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
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
	prog := resolve.ResolveFor(srcs, opts.Opt == "release")
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
	if err := opt.Run(mod, opt.ExecOptionsFor(opts.Opt)); err != nil {
		return nil, fmt.Errorf("ir: %v", err)
	}
	return nil, BuildModule(mod, srcs, out, opts)
}

// BuildModule compiles and links an already lowered and optimized module
// (Build's back half): `kern test` lowers with its test functions as roots
// and builds the result (specs/test-command/SPEC.md). srcs supply the
// debug-info sources. A module with test functions builds their selecting
// entry in place of main's, hosted only.
func BuildModule(mod *ir.Module, srcs []*source.File, out string, opts Options) error {
	if mod.Entry == "" && len(mod.Tests) == 0 {
		return errors.New("no main function")
	}
	if len(mod.Tests) > 0 && opts.Freestanding {
		return errors.New("test executables are hosted only")
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
		lo.Sources["std/core.kern"] = resolve.Prelude(opts.Opt == "release")
	}
	text, errs := llvm.EmitWith(mod, lo)
	if len(errs) > 0 {
		return fmt.Errorf("backend: %v", errs[0])
	}
	if opts.EmitLLVM != "" {
		if err := os.WriteFile(opts.EmitLLVM, []byte(text), 0o644); err != nil {
			return err
		}
	}
	if opts.Freestanding {
		return buildFreestanding(text, out, triple, opts.Opt == "release")
	}
	clang, err := exec.LookPath("clang")
	if err != nil {
		return ErrNoClang
	}
	dir, err := os.MkdirTemp("", "kern-build-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ll := filepath.Join(dir, "prog.ll")
	if err := os.WriteFile(ll, []byte(text), 0o644); err != nil {
		return err
	}
	level := "-O1"
	if opts.Opt == "release" {
		level = "-O2"
	}
	// the runtime's own flags, everything its object depends on; frame
	// pointers in it too: the fault handler walks the chain through it
	// (specs/native-trap-loc)
	rtFlags := []string{level, "-ffp-model=strict", "-fno-omit-frame-pointer"}
	if opts.Opt == "release" {
		rtFlags = append(rtFlags, "-DKERN_RELEASE") // identity tags off (PLAN.md 2.2)
	}
	if opts.TestHooks {
		rtFlags = append(rtFlags, "-DKERN_TEST_HOOKS")
	}
	if opts.TSan {
		rtFlags = append(rtFlags, "-fsanitize=thread", "-g")
	}
	if opts.Debug {
		rtFlags = append(rtFlags, "-g", "-O0")
	}
	if triple != HostTriple() {
		rtFlags = append(rtFlags, "-target", triple)
	}
	rt := runtimeObject(clang, rtFlags)
	if rt == "" {
		rt = filepath.Join(dir, "kern_rt.c")
		if err := os.WriteFile(rt, []byte(hosted.Source), 0o644); err != nil {
			return err
		}
	}
	args := []string{level, "-Wno-override-module", "-ffp-model=strict", "-fno-omit-frame-pointer", "-o", out, ll, rt}
	if opts.Opt == "release" {
		args = append(args, "-DKERN_RELEASE")
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
	args = append(args, "-lpthread", "-lm") // libm: float intrinsics lower to trunc, fmod, ...
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
		return fmt.Errorf("clang failed: %v: %s", err, msg)
	}
	return nil
}

// runtimeObject is the hosted runtime compiled with flags: from the
// user's cache directory, compiled into it on a miss, or "" when no cache
// is usable (KERN_NO_RUNTIME_CACHE set, no cache directory, a failed
// compile), and the caller compiles the source with the program. The
// runtime is half of what clang does for a small program, and it changes
// only with the compiler.
//
// The key covers the source, the flags, the clang binary (its resolved
// path, size and modification time) and the working directory, which
// the compile runs in as the program's does. Under the key a manifest
// names the object and records:
//   - the fingerprint of the compile: the preprocessed source and the
//     driver's account of the job (`clang -E -v`: its version, the
//     configuration files it loaded, the -cc1 command with every option
//     they expand to). Every build takes the fingerprint again, one
//     preprocessing run, and compiles afresh when it changed. So a
//     header edited, created where it would shadow another or probed by
//     __has_include, another SDK or environment, a configuration file
//     created or edited, all compile afresh.
//   - the size and modification time of the compiler executable behind
//     any shim and of the configuration files, which a compiler upgraded
//     in place changes when the fingerprint does not.
//
// The fingerprint and the compile name a copy of the source kept under
// the key, so their paths do not vary between builds. Objects are named
// by their content and never rewritten, and the manifest is published by
// one rename, so a concurrent build sees either the old entry or the new
// one, whole.
func runtimeObject(clang string, flags []string) string {
	if os.Getenv("KERN_NO_RUNTIME_CACHE") != "" {
		return ""
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	bin, err := filepath.EvalSymlinks(clang)
	if err != nil {
		return ""
	}
	fi, err := os.Stat(bin)
	if err != nil {
		return ""
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00wd=%s\x00", bin, fi.Size(), fi.ModTime().UnixNano(), wd) // a hash never fails a write
	for _, f := range flags {
		_, _ = fmt.Fprintf(h, "%s\x00", f)
	}
	h.Write([]byte(hosted.Source))
	dir := filepath.Join(base, "kern", "runtime")
	key := fmt.Sprintf("%x", h.Sum(nil)[:16])
	manifest, src := filepath.Join(dir, key+".deps"), filepath.Join(dir, key+".c")
	fingerprint := func() (string, string, bool) {
		cmd := exec.Command(clang, append(append([]string{}, flags...), "-E", "-v", "-o", "-", src)...)
		var out, job bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &job
		if err := cmd.Run(); err != nil {
			return "", "", false
		}
		f := sha256.New()
		f.Write(out.Bytes())
		f.Write([]byte{0})
		f.Write(job.Bytes())
		return fmt.Sprintf("%x", f.Sum(nil)), job.String(), true
	}
	if obj, want, ok := cachedObject(dir, manifest); ok {
		if got, _, ok := fingerprint(); ok && got == want {
			return obj
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	tmp, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		return ""
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// the source copy under the key: written whole, then renamed into place
	ts := filepath.Join(tmp, "kern_rt.c")
	if err := os.WriteFile(ts, []byte(hosted.Source), 0o644); err != nil {
		return ""
	}
	if err := os.Rename(ts, src); err != nil {
		return ""
	}
	// fingerprint first: a change after it shows at the next build
	fp, job, ok := fingerprint()
	if !ok {
		return ""
	}
	driver := driverFiles(job)
	if len(driver) == 0 {
		return "" // the compiler that would run is unknown
	}
	o := filepath.Join(tmp, "kern_rt.o")
	if err := exec.Command(clang, append(append([]string{}, flags...), "-c", "-o", o, src)...).Run(); err != nil {
		return ""
	}
	code, err := os.ReadFile(o)
	if err != nil {
		return ""
	}
	name := fmt.Sprintf("%s-%x.o", key, sha256.Sum256(code))
	var m strings.Builder
	fmt.Fprintf(&m, "%s\n%s\n", name, fp)
	for _, path := range driver {
		if !filepath.IsAbs(path) {
			path = filepath.Join(wd, path)
		}
		fi, err := os.Stat(path)
		if err != nil {
			return ""
		}
		fmt.Fprintf(&m, "%d %d %s\n", fi.Size(), fi.ModTime().UnixNano(), path)
	}
	mt := filepath.Join(tmp, "kern_rt.deps")
	if err := os.WriteFile(mt, []byte(m.String()), 0o644); err != nil {
		return ""
	}
	obj := filepath.Join(dir, name)
	if err := os.Rename(o, obj); err != nil {
		return ""
	}
	if err := os.Rename(mt, manifest); err != nil {
		return ""
	}
	return obj
}

// cachedObject is the object a manifest names and the fingerprint it
// records, when the object exists and every file the manifest lists is
// unchanged.
func cachedObject(dir, manifest string) (string, string, bool) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", "", false
	}
	name, rest, ok := strings.Cut(string(data), "\n")
	if !ok || name == "" || strings.ContainsRune(name, filepath.Separator) {
		return "", "", false
	}
	fp, files, ok := strings.Cut(rest, "\n")
	if !ok || fp == "" {
		return "", "", false
	}
	obj := filepath.Join(dir, name)
	if _, err := os.Stat(obj); err != nil || !filesUnchanged(files) {
		return "", "", false
	}
	return obj, fp, true
}

// driverFiles are the files a driver's account of its job (`clang -v` or
// `-###`) shows the compile depends on beyond the source and headers:
// each configuration file it loaded, and the executable of the -cc1 job,
// the compiler that runs behind any shim. Empty when no -cc1 job is
// listed.
func driverFiles(job string) []string {
	var files []string
	cc1 := false
	for _, line := range strings.Split(job, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Configuration file: "); ok {
			files = append(files, rest)
		} else if strings.HasPrefix(line, `"`) && (strings.Contains(line, `" -cc1 `) || strings.Contains(line, `" "-cc1" `)) {
			if end := strings.Index(line[1:], `"`); end > 0 {
				files = append(files, line[1:1+end])
				cc1 = true
			}
		}
	}
	if !cc1 {
		return nil
	}
	return files
}

// filesUnchanged reports whether every file of a manifest's lines ("size
// mtime path") still has the recorded size and modification time; a
// malformed line is a change.
func filesUnchanged(lines string) bool {
	for _, line := range strings.Split(strings.TrimSuffix(lines, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return false
		}
		size, err1 := strconv.ParseInt(parts[0], 10, 64)
		mtime, err2 := strconv.ParseInt(parts[1], 10, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		fi, err := os.Stat(parts[2])
		if err != nil || fi.Size() != size || fi.ModTime().UnixNano() != mtime {
			return false
		}
	}
	return true
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
	if err := opt.Run(mod, opt.Options{}); err != nil {
		return "", nil, fmt.Errorf("ir: %v", err)
	}
	if triple == "" {
		triple = HostTriple()
	}
	text, errs := llvm.Emit(mod, triple)
	if len(errs) > 0 {
		return "", nil, fmt.Errorf("backend: %v", errs[0])
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
