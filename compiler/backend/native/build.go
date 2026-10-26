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
	}
	text, errs := llvm.Emit(mod, triple)
	if len(errs) > 0 {
		return nil, fmt.Errorf("backend: %v", errs[0])
	}
	text = withProgramName(text, srcs[0].Name)
	if opts.EmitLLVM != "" {
		if err := os.WriteFile(opts.EmitLLVM, []byte(text), 0o644); err != nil {
			return nil, err
		}
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
		text = withProgramName(text, srcs[0].Name)
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
func withProgramName(text, name string) string {
	esc := strings.NewReplacer("\\", "\\5C", "\"", "\\22").Replace(name)
	global := fmt.Sprintf("@kern.progname = private constant [%d x i8] c\"%s\\00\"\n", len(name)+1, esc)
	text = strings.Replace(text, "declare void @kern_start()", "declare void @kern_start()\ndeclare void @kern_set_file(ptr)", 1)
	return strings.Replace(text, "define i32 @main() {\nentry:\n", global+"define i32 @main() {\nentry:\n  call void @kern_set_file(ptr @kern.progname)\n", 1)
}
