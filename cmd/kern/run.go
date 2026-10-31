package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// BuildOptions are the configuration dimensions of PLAN.md §0.
type BuildOptions struct {
	Opt string // debug | release
	// NoStackCheck drops the freestanding prologue stack check (an unsafe
	// build option: stack overflow becomes a trusted-boundary obligation).
	NoStackCheck bool
	// ElideChecks drops runtime checks: measurement only (PLAN.md 4.4).
	ElideChecks bool
	// Debug emits DWARF line information.
	Debug      bool
	Env        string // hosted | freestanding
	TestHooks  bool
	StackBytes int
	// optSet and envSet record an explicit --opt or --env, which overrides
	// the manifest's value (specs/module-discovery/SPEC.md R23).
	optSet, envSet bool
}

// applyManifest takes the manifest's opt and env where no flag set them.
func (o *BuildOptions) applyManifest(m *project.Manifest) {
	if m == nil {
		return
	}
	if !o.optSet && m.Opt != nil {
		o.Opt = *m.Opt
	}
	if !o.envSet && m.Env != nil {
		o.Env = *m.Env
	}
}

// load discovers the program's files from the named ones, or from the
// manifest when none is named, and writes the single JSON response for a
// failure. It returns the result and whether the caller may go on.
func load(files []string, stdout io.Writer) (project.Result, bool) {
	cwd, _ := os.Getwd()
	pr := project.Load(cwd, files)
	if len(pr.Diags) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: pr.Diags})
		return pr, false
	}
	return pr, true
}

// Build resolves, checks, lowers and validates a program for the analysis
// commands. Diagnostics are returned for user errors; a validator failure
// is an internal error.
func Build(srcs []*source.File, optLevel string) (*ir.Module, []diag.Diagnostic, error) {
	return buildWith(srcs, optLevel, opt.OptionsFor(optLevel))
}

// buildExec is Build for a program that runs: with the library inliner in
// release (specs/inline-library).
func buildExec(srcs []*source.File, optLevel string) (*ir.Module, []diag.Diagnostic, error) {
	return buildWith(srcs, optLevel, opt.ExecOptionsFor(optLevel))
}

func buildWith(srcs []*source.File, optLevel string, o opt.Options) (*ir.Module, []diag.Diagnostic, error) {
	prog := resolve.ResolveFor(srcs, optLevel == "release")
	if len(prog.Diags) > 0 {
		return nil, prog.Diags, nil
	}
	if ds := check.Check(prog); len(ds) > 0 {
		return nil, ds, nil
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		return nil, nil, &InternalError{Err: fmt.Errorf("%s", ds[0].Message)}
	}
	if err := opt.Run(mod, o); err != nil {
		return nil, nil, &InternalError{Err: fmt.Errorf("IR validation after %v", err)}
	}
	return mod, nil, nil
}

// cliOpt is the `--opt` level of the current machine command, set by main
// before dispatch (the §16 request carries no configuration).
var cliOpt = "debug"

// parseRunArgs separates the run subcommand's flags, files and program args.
func parseRunArgs(args []string) (BuildOptions, []string, []string, error) {
	opts := BuildOptions{Opt: "debug", Env: "hosted", StackBytes: 8 << 20}
	var files, prog []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			prog = args[i+1:]
			i = len(args)
		case a == "--test-hooks":
			opts.TestHooks = true
		case a == "--unsafe-no-stack-check":
			opts.NoStackCheck = true
		case a == "--unsafe-elide-checks":
			opts.ElideChecks = true
		case a == "--debug":
			opts.Debug = true
		case strings.HasPrefix(a, "--opt="), strings.HasPrefix(a, "--env="), strings.HasPrefix(a, "--stack-bytes="):
			k, v, _ := strings.Cut(a, "=")
			switch k {
			case "--opt":
				if v != "debug" && v != "release" {
					return opts, nil, nil, fmt.Errorf("--opt must be debug or release")
				}
				opts.Opt, opts.optSet = v, true
			case "--env":
				if v != "hosted" && v != "freestanding" {
					return opts, nil, nil, fmt.Errorf("--env must be hosted or freestanding")
				}
				opts.Env, opts.envSet = v, true
			case "--stack-bytes":
				n, err := strconv.Atoi(v)
				if err != nil || n <= 0 {
					return opts, nil, nil, fmt.Errorf("--stack-bytes must be a positive integer")
				}
				opts.StackBytes = n
			}
		case strings.HasPrefix(a, "-"):
			return opts, nil, nil, fmt.Errorf("unknown flag %s", a)
		default:
			files = append(files, a)
		}
	}
	if opts.TestHooks && opts.Opt == "release" {
		return opts, nil, nil, fmt.Errorf("--test-hooks requires --opt debug")
	}
	return opts, files, prog, nil // no file named: the manifest's entry (project.Load)
}

// runProgram implements `kern run`: check failures write the JSON response
// to stdout and exit 2; otherwise stdout belongs to the program, a trap
// writes its record to stderr and exits 134.
func runProgram(args []string, stdout, stderr io.Writer) int {
	opts, named, progArgs, err := parseRunArgs(args)
	if err != nil {
		return fail(stdout, "CLI_INVALID_FLAGS", "%v", err)
	}
	pr, ok := load(named, stdout)
	if !ok {
		return 2
	}
	opts.applyManifest(pr.Manifest)
	if opts.TestHooks && opts.Opt == "release" {
		return fail(stdout, "CLI_INVALID_FLAGS", "--test-hooks requires --opt debug")
	}
	srcs, files := pr.Files, pr.Names()
	mod, ds, err := buildExec(srcs, opts.Opt)
	req := Request{SchemaVersion: diag.SchemaVersion, Command: "run", Files: files}
	if err != nil {
		return internal(req, diag.SchemaVersion, err, stdout, stderr)
	}
	if len(ds) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	if mod.Entry == "" {
		return fail(stdout, "CLI_INVALID_FLAGS", "program has no main function")
	}
	it := interp.New(mod, files[0], stdout, stderr)
	it.SetArgs(append([]string{files[0]}, progArgs...))
	it.TestHooks = opts.TestHooks
	it.Freestanding = opts.Env == "freestanding"
	it.SetIdentityTags(opts.Opt != "release")
	it.SetStackLimit(opts.StackBytes)
	return it.Run()
}

// emitIR implements the emit-ir machine command.
func emitIR(req Request, wire string, srcs []*source.File, stdout, stderr io.Writer) int {
	mod, ds, err := Build(srcs, cliOpt)
	if err != nil {
		return internal(req, wire, err, stdout, stderr)
	}
	if len(ds) > 0 {
		_ = diag.WriteAt(stdout, wire, diag.Response{OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	resp := diag.Response{OK: true, Result: diag.EmitIRResult{IR: ir.Print(mod)}, Diagnostics: []diag.Diagnostic{}}
	if err := diag.WriteAt(stdout, wire, resp); err != nil {
		return 3
	}
	return 0
}
