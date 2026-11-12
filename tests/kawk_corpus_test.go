package tests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/project"
)

// kawkCase is one case of examples/kawk/corpus: an AWK program, what it is
// run with, and what the system awk printed and returned for it
// (examples/kawk/corpus/README.md).
type kawkCase struct {
	name   string
	prog   string // cases/NAME.awk
	status int    // the exit status awk gave
	host   bool   // its record holds the output of macOS's own commands
}

// kawkCaseLimit is how long one case may take: the limit record.py gives
// the system awk.
const kawkCaseLimit = 20 * time.Second

// kawkCases reads status.txt, which names every case, and checks that the
// case files and the list agree.
func kawkCases(t *testing.T, corpus string) []kawkCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpus, "status.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		t.Fatal("status.txt is empty: run record.py")
	}
	var cases []kawkCase
	listed := map[string]bool{}
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || (f[2] != "host" && f[2] != "-") {
			t.Fatalf("status.txt line %d: want `name status host|-`, have %q", i+1, line)
		}
		status, err := strconv.Atoi(f[1])
		if err != nil {
			t.Fatalf("status.txt line %d: %v", i+1, err)
		}
		c := kawkCase{name: f[0], prog: filepath.Join(corpus, "cases", f[0]+".awk"), status: status, host: f[2] == "host"}
		for _, need := range []string{c.prog, strings.TrimSuffix(c.prog, ".awk") + ".out"} {
			if _, err := os.Stat(need); err != nil {
				t.Fatalf("status.txt names %s: %v", f[0], err)
			}
		}
		listed[f[0]] = true
		cases = append(cases, c)
	}
	progs, _ := filepath.Glob(filepath.Join(corpus, "cases", "*.awk"))
	for _, p := range progs {
		if name := strings.TrimSuffix(filepath.Base(p), ".awk"); !listed[name] {
			t.Fatalf("case %s is not in status.txt: run record.py", name)
		}
	}
	return cases
}

// kawkCommand is the arguments and the standard input of a case run in
// workdir, as record.py's command builds them: the options before a line
// "--" of NAME.args go before -f, the operands after it; @IN in an operand
// is the case's input, copied into the working directory as in.txt;
// @PROG2 is prog2.awk.
func kawkCommand(corpus string, c kawkCase, workdir string) (args []string, stdin string, err error) {
	base := strings.TrimSuffix(c.prog, ".awk")
	stdin = base + ".in"
	if _, e := os.Stat(stdin); e != nil {
		stdin = filepath.Join(corpus, "input.txt")
	}
	var opts, opers []string
	if b, e := os.ReadFile(base + ".args"); e == nil {
		lines := strings.Split(string(b), "\n")
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		opers = lines
		for i, l := range lines {
			if l == "--" {
				opts, opers = lines[:i], lines[i+1:]
				break
			}
		}
	}
	copied := false
	for i, o := range opers {
		if strings.Contains(o, "@IN") {
			if !copied {
				in, e := os.ReadFile(stdin)
				if e != nil {
					return nil, "", e
				}
				if e := os.WriteFile(filepath.Join(workdir, "in.txt"), in, 0o644); e != nil {
					return nil, "", e
				}
				copied = true
			}
			opers[i] = strings.ReplaceAll(o, "@IN", "in.txt")
		}
	}
	for i, o := range opts {
		opts[i] = strings.ReplaceAll(o, "@PROG2", filepath.Join(corpus, "prog2.awk"))
	}
	args = append(append(append(args, opts...), "-f", c.prog), opers...)
	return args, stdin, nil
}

// runKawkCase runs one case and returns what is wrong with it, "" when its
// output and status are the record's.
func runKawkCase(corpus, kawk, scratch string, c kawkCase) string {
	// the directory is named as record.py names it: one case asks for it
	parent, err := os.MkdirTemp(scratch, "case-")
	if err != nil {
		return err.Error()
	}
	defer func() { _ = os.RemoveAll(parent) }()
	workdir := filepath.Join(parent, "kawkcase")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		return err.Error()
	}
	args, stdinPath, err := kawkCommand(corpus, c, workdir)
	if err != nil {
		return err.Error()
	}
	want, err := os.ReadFile(strings.TrimSuffix(c.prog, ".awk") + ".out")
	if err != nil {
		return err.Error()
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = stdin.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), kawkCaseLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, kawk, args...)
	cmd.Dir = workdir
	cmd.Stdin = stdin
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.Env = kawkEnv()
	// the case and whatever it starts are one process group, killed
	// together at the limit so that no command outlives its case
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Sprintf("timed out after %s", kawkCaseLimit)
	}
	status := 0
	var ee *exec.ExitError
	switch {
	case errors.As(runErr, &ee):
		status = ee.ExitCode()
	case runErr != nil:
		return runErr.Error()
	}
	if status == c.status && bytes.Equal(out.Bytes(), want) {
		return ""
	}
	var msg strings.Builder
	fmt.Fprintf(&msg, "status %d, want %d", status, c.status)
	if !bytes.Equal(out.Bytes(), want) {
		a, b := bytes.Split(want, []byte("\n")), bytes.Split(out.Bytes(), []byte("\n"))
		for k := 0; k < len(a) || k < len(b); k++ {
			x, y := []byte("<none>"), []byte("<none>")
			if k < len(a) {
				x = a[k]
			}
			if k < len(b) {
				y = b[k]
			}
			if !bytes.Equal(x, y) {
				fmt.Fprintf(&msg, "; line %d: awk %.150q, kawk %.150q", k+1, x, y)
				break
			}
		}
	}
	if errOut.Len() > 0 {
		fmt.Fprintf(&msg, "; stderr %.200q", errOut.Bytes())
	}
	return msg.String()
}

// kawkEnv is the environment the records were made under.
func kawkEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch name, _, _ := strings.Cut(kv, "="); name {
		case "LC_ALL", "KAWK_TEST", "TZ", "POSIXLY_CORRECT":
		default:
			env = append(env, kv)
		}
	}
	return append(env, "LC_ALL=C", "KAWK_TEST=yes", "TZ=UTC")
}

// TestKawkCorpus runs examples/kawk, built natively at release, on every
// case of its corpus and compares the standard output and the exit status,
// byte for byte, with what the system awk gave when the case was recorded
// (specs/std-regex/SPEC.md R22). It needs no awk. The cases marked host
// start commands or read the environment, so their records hold what
// macOS's own commands print: they run on macOS and are skipped, and
// counted, everywhere else.
func TestKawkCorpus(t *testing.T) {
	if _, err := exec.LookPath("clang"); err != nil {
		t.Skip("no clang")
	}
	corpus, err := filepath.Abs(filepath.Join("..", "examples", "kawk", "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	cases := kawkCases(t, corpus)

	_, files := loadFixture(t, filepath.Join("..", "examples", "kawk"))
	kawk := filepath.Join(t.TempDir(), "kawk")
	// the manifest's stack size is part of the program: an AWK function
	// calls itself 5,000 deep in some cases
	manifestPath := filepath.Join("..", "examples", "kawk", project.ManifestName)
	mb, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, mds := project.ParseManifest(manifestPath, mb)
	if len(mds) > 0 || manifest.StackBytes == nil {
		t.Fatalf("%s: %v (and it must give stack_bytes)", manifestPath, mds)
	}
	if ds, err := native.Build(files, kawk, native.Options{Opt: "release", StackBytes: uint64(*manifest.StackBytes)}); err != nil || len(ds) > 0 {
		t.Fatalf("building kawk: %v %v", err, ds)
	}

	scratch := t.TempDir()
	type result struct {
		name, what string
	}
	var (
		mu      sync.Mutex
		failed  []result
		ran     int
		skipped int
	)
	queue := make(chan kawkCase)
	var wg sync.WaitGroup
	for range testJobs(t) {
		wg.Go(func() {
			for c := range queue {
				what := runKawkCase(corpus, kawk, scratch, c)
				mu.Lock()
				ran++
				if what != "" {
					failed = append(failed, result{c.name, what})
				}
				mu.Unlock()
			}
		})
	}
	for _, c := range cases {
		if c.host && runtime.GOOS != "darwin" {
			skipped++
			continue
		}
		queue <- c
	}
	close(queue)
	wg.Wait()

	sort.Slice(failed, func(i, j int) bool { return failed[i].name < failed[j].name })
	for _, f := range failed {
		t.Errorf("%s: %s", f.name, f.what)
	}
	t.Logf("%d cases run, %d differ, %d skipped (their records are macOS's own commands' output)", ran, len(failed), skipped)
}
