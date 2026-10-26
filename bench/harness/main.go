// Command harness builds, runs and reports the benchmark workloads of
// PLAN.md 2.6 and 3.4 (see bench/PROTOCOL.md for the per-program output line).
//
//	go run ./bench/harness [-runs 5] [-out bench/REPORT.md] [-only workload[,...]]
//	    [-lang c,rust,kern] [-experiment]
//
// Rows are grouped by workload, then build configuration, then language.
// C is built with `clang -O2`, Rust with `cargo build --release`, kern with
// `kern build --opt=release` and `--opt=debug` (the driver is built once from
// ./cmd/kern). Every `*.kern` file in bench/kern/<workload>/ is a separate
// row named by its file stem (e.g. packet_parser and packet_parser_simd).
// Rows whose implementation directory does not exist are skipped and listed
// in the report. Workloads with a failure path (bench/PROTOCOL.md) are run
// once more with KERN_BENCH_FAIL=1 and the ` failures=<N>` field they print
// is reported in the "failure path" column.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var allWorkloads = []string{"vector", "hash_table", "btree", "arena_ast", "ring_buffer", "wal", "packet_parser",
	"buffer_pool", "page_frame_allocator", "intrusive_run_queue", "thread_pool", "lock_protected_map", "lock_free_queue"}

// failureWorkloads lists the workloads whose programs exercise a failure
// path when KERN_BENCH_FAIL=1 is set (bench/PROTOCOL.md).
var failureWorkloads = map[string]bool{"buffer_pool": true, "page_frame_allocator": true, "lock_free_queue": true}

// workloadThreads is the thread count of each workload ("1" when absent).
var workloadThreads = map[string]string{"thread_pool": "4+1", "lock_protected_map": "4", "lock_free_queue": "2"}

func threadsOf(workload string) string {
	if t, ok := workloadThreads[workload]; ok {
		return t
	}
	return "1"
}

// slowRun is the per-run wall time above which the report flags a workload.
const slowRun = 5 * time.Second

// runResult is one parsed protocol line plus the child's peak RSS in bytes.
type runResult struct {
	ops, ns, p50, p99, checksum uint64
	failures                    uint64
	hasFailures                 bool // the line carried a failures= field
	rss                         float64
	wall                        time.Duration
}

// row is one (workload, configuration, program) measurement; name is the
// program's name (the workload for C and Rust, the file stem for kern).
type row struct {
	workload, cfg, lang string
	name                string
	dir                 string
	sources             []string
	kernFile            string
	bin                 string
	skip                string // non-empty: the row was not built or run
	buildErr            string
	compile             time.Duration
	text                int64
	textNote            string
	unsafeLines         int
	allocs, copies      string
	places              string
	runs                []runResult
	errs                []string
	failRun             *runResult // the KERN_BENCH_FAIL=1 run, when made
	failErr             string
}

// label names the row's program within its workload: the language, plus the
// file stem for kern programs that are not the workload's main file.
func (r *row) label() string {
	if r.name == "" || r.name == r.workload {
		return r.lang
	}
	return r.lang + " (" + r.name + ")"
}

func (r *row) key() string { return r.label() + "/" + r.cfg }

// isMain reports the workload's main program (C and Rust rows, and the kern
// file named after the workload).
func (r *row) isMain() bool { return r.name == "" || r.name == r.workload }

// isFailureTwin reports a kern `<w>_fail.kern` program, whose normal run is
// the failure path (kern has no environment access).
func (r *row) isFailureTwin() bool { return strings.HasSuffix(r.name, "_fail") }

type toolchain struct {
	clang, rustc, cargo, goVer string
	warnings                   []string
}

var protoRE = regexp.MustCompile(`^ops=(\d+) ns=(\d+) p50=(\d+) p99=(\d+) checksum=(\d+)(?: failures=(\d+))?$`)

func main() {
	runs := flag.Int("runs", 5, "repetitions per row")
	out := flag.String("out", "bench/REPORT.md", "report path (relative to the repository root)")
	only := flag.String("only", "", "comma-separated workloads to run (default: all)")
	langs := flag.String("lang", "c,rust,kern", "comma-separated languages to run")
	experiment := flag.Bool("experiment", false, "run the [unproven] aliasing experiment on the vector workload")
	flag.Parse()
	if *runs < 1 {
		fatalf("-runs must be at least 1")
	}

	root, err := findRoot()
	if err != nil {
		fatalf("%v", err)
	}
	buildDir := filepath.Join(root, "bench", ".build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		fatalf("%v", err)
	}

	tc := readToolchain(root)
	for _, w := range tc.warnings {
		logf("warning: %s", w)
	}

	workloads := allWorkloads
	if *only != "" {
		workloads = nil
		for _, w := range strings.Split(*only, ",") {
			w = strings.TrimSpace(w)
			if !contains(allWorkloads, w) {
				fatalf("unknown workload %q", w)
			}
			workloads = append(workloads, w)
		}
	}
	wantLang := map[string]bool{}
	for _, l := range strings.Split(*langs, ",") {
		l = strings.TrimSpace(l)
		if l != "c" && l != "rust" && l != "kern" {
			fatalf("unknown language %q", l)
		}
		wantLang[l] = true
	}

	rows := planRows(root, workloads, wantLang)
	kernBin := ""
	if needsKern(rows) {
		kernBin = filepath.Join(buildDir, "kern")
		if msg, err := runCmd(root, "go", "build", "-o", kernBin, "./cmd/kern"); err != nil {
			logf("building kern driver failed: %v: %s", err, msg)
			for _, r := range rows {
				if r.lang == "kern" && r.skip == "" {
					r.skip = "kern driver failed to build"
				}
			}
			kernBin = ""
		}
	}

	for _, r := range rows {
		if r.skip != "" {
			logf("skip %s/%s: %s", r.workload, r.key(), r.skip)
			continue
		}
		logf("build %s/%s", r.workload, r.key())
		build(root, buildDir, kernBin, r)
		if r.buildErr != "" {
			logf("  build failed: %s", firstLine(r.buildErr))
			continue
		}
		for i := 0; i < *runs; i++ {
			res, err := runOnce(root, r.bin, nil)
			if err != nil {
				r.errs = append(r.errs, err.Error())
				logf("  run %d failed: %v", i+1, err)
				continue
			}
			r.runs = append(r.runs, res)
		}
		if len(r.runs) > 0 {
			logf("  %s", summary(r))
		}
		if failureWorkloads[r.workload] {
			res, err := runOnce(root, r.bin, []string{"KERN_BENCH_FAIL=1"})
			switch {
			case err != nil:
				r.failErr = err.Error()
				logf("  failure-path run failed: %v", err)
			case !res.hasFailures:
				logf("  failure-path run: no failures field")
			default:
				r.failRun = &res
				logf("  failure-path run: failures=%d checksum=%d", res.failures, res.checksum)
			}
		}
	}

	exp := ""
	if *experiment {
		exp = aliasingExperiment(root, buildDir, kernBin, *runs, rows)
	}

	report := writeReport(root, tc, workloads, rows, *runs, exp)
	outPath := *out
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(root, outPath)
	}
	if err := os.WriteFile(outPath, []byte(report), 0o644); err != nil {
		fatalf("writing report: %v", err)
	}
	logf("wrote %s", outPath)
}

// planRows lists every row for the requested workloads and languages,
// marking rows whose sources do not exist as skipped.
func planRows(root string, workloads []string, want map[string]bool) []*row {
	var rows []*row
	for _, w := range workloads {
		if want["c"] {
			r := &row{workload: w, cfg: "release", lang: "c", name: w, dir: filepath.Join(root, "bench", "c", w)}
			main := filepath.Join(r.dir, "main.c")
			if !exists(main) {
				r.skip = "bench/c/" + w + "/main.c not found"
			}
			r.sources = append(globFiles(r.dir, "*.c", "*.h"), filepath.Join(root, "bench", "c", "bench.h"))
			rows = append(rows, r)
		}
		if want["rust"] {
			r := &row{workload: w, cfg: "release", lang: "rust", name: w, dir: filepath.Join(root, "bench", "rust", w)}
			if !exists(filepath.Join(r.dir, "Cargo.toml")) {
				r.skip = "bench/rust/" + w + "/Cargo.toml not found"
			}
			r.sources = append(globFiles(filepath.Join(r.dir, "src"), "*.rs"), filepath.Join(root, "bench", "rust", "bench.rs"))
			rows = append(rows, r)
		}
		if want["kern"] {
			dir := filepath.Join(root, "bench", "kern", w)
			files := globFiles(dir, "*.kern")
			// The workload's main file comes first; the other programs
			// follow in name order.
			sort.SliceStable(files, func(i, j int) bool {
				return filepath.Base(files[i]) == w+".kern" && filepath.Base(files[j]) != w+".kern"
			})
			if len(files) == 0 {
				for _, cfg := range []string{"release", "debug"} {
					r := &row{workload: w, cfg: cfg, lang: "kern", name: w, dir: dir}
					if exists(dir) {
						r.skip = "no .kern file in bench/kern/" + w + "/"
					} else {
						r.skip = "bench/kern/" + w + "/ not found"
					}
					rows = append(rows, r)
				}
			}
			for _, f := range files {
				for _, cfg := range []string{"release", "debug"} {
					r := &row{workload: w, cfg: cfg, lang: "kern", dir: dir, kernFile: f, sources: []string{f}}
					r.name = strings.TrimSuffix(filepath.Base(f), ".kern")
					rows = append(rows, r)
				}
			}
		}
	}
	return rows
}

func needsKern(rows []*row) bool {
	for _, r := range rows {
		if r.lang == "kern" && r.skip == "" {
			return true
		}
	}
	return false
}

// build compiles one row, recording compile time and the static metrics.
func build(root, buildDir, kernBin string, r *row) {
	dir := filepath.Join(buildDir, r.workload)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.buildErr = err.Error()
		return
	}
	r.bin = filepath.Join(dir, r.lang+"-"+r.cfg)
	if r.name != "" && r.name != r.workload {
		r.bin = filepath.Join(dir, r.lang+"-"+r.name+"-"+r.cfg)
	}
	_ = os.Remove(r.bin)
	start := time.Now()
	var msg string
	var err error
	switch r.lang {
	case "c":
		msg, err = runCmd(root, "clang", "-O2", "-o", r.bin, filepath.Join(r.dir, "main.c"))
	case "rust":
		target := filepath.Join(dir, "cargo")
		_ = os.RemoveAll(target)
		msg, err = runCmdEnv(r.dir, []string{"CARGO_TARGET_DIR=" + target}, "cargo", "build", "--release", "--quiet")
		if err == nil {
			err = os.Rename(filepath.Join(target, "release", "bench_"+r.workload), r.bin)
		}
	case "kern":
		msg, err = runCmd(root, kernBin, "build", "--opt="+r.cfg, "-o", r.bin, r.kernFile)
	}
	r.compile = time.Since(start)
	if err != nil {
		r.buildErr = fmt.Sprintf("%v: %s", err, msg)
		return
	}
	r.text, r.textNote = textBytes(r.bin)
	r.unsafeLines = unsafeLines(r.sources)
	r.allocs, r.copies, r.places = "n/a", "n/a", "n/a"
	if r.lang == "kern" {
		r.allocs = kernCount(root, kernBin, "show-allocations", r.kernFile, "sites")
		r.copies = kernCount(root, kernBin, "show-copies", r.kernFile, "copies")
		r.places = kernCount(root, kernBin, "explain-ownership", r.kernFile, "places")
	}
}

// runOnce executes a benchmark binary with extra environment variables and
// parses its protocol line.
func runOnce(root, bin string, env []string) (runResult, error) {
	cmd := exec.Command(bin)
	cmd.Dir = root
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	wall := time.Since(start)
	if err != nil {
		return runResult{}, fmt.Errorf("%s: %v: %s", filepath.Base(bin), err, firstLine(stderr.String()))
	}
	var res runResult
	found := false
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		m := protoRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		res.ops, _ = strconv.ParseUint(m[1], 10, 64)
		res.ns, _ = strconv.ParseUint(m[2], 10, 64)
		res.p50, _ = strconv.ParseUint(m[3], 10, 64)
		res.p99, _ = strconv.ParseUint(m[4], 10, 64)
		res.checksum, _ = strconv.ParseUint(m[5], 10, 64)
		if m[6] != "" {
			res.failures, _ = strconv.ParseUint(m[6], 10, 64)
			res.hasFailures = true
		}
		found = true
		break
	}
	if !found {
		return runResult{}, fmt.Errorf("%s: no protocol line in output %q", filepath.Base(bin), firstLine(stdout.String()))
	}
	if res.ns == 0 {
		return runResult{}, fmt.Errorf("%s: ns=0", filepath.Base(bin))
	}
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		res.rss = float64(ru.Maxrss)
		if runtime.GOOS == "linux" {
			res.rss *= 1024 // kilobytes on Linux, bytes on macOS
		}
	}
	res.wall = wall
	return res, nil
}

// textBytes returns the size of the binary's text section from size(1),
// falling back to the file size with a note.
func textBytes(bin string) (int64, string) {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("size", "-m", bin).Output(); err == nil {
			if m := regexp.MustCompile(`Section __text: (\d+)`).FindSubmatch(out); m != nil {
				n, _ := strconv.ParseInt(string(m[1]), 10, 64)
				return n, ""
			}
		}
	} else if out, err := exec.Command("size", bin).Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 2 {
			if f := strings.Fields(lines[1]); len(f) > 0 {
				if n, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					return n, ""
				}
			}
		}
	}
	st, err := os.Stat(bin)
	if err != nil {
		return 0, "unknown"
	}
	return st.Size(), "file size; size(1) unavailable"
}

var unsafeRE = regexp.MustCompile(`\bunsafe\b`)

// unsafeLines counts source lines containing the word "unsafe".
func unsafeLines(files []string) int {
	n := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if unsafeRE.MatchString(line) {
				n++
			}
		}
	}
	return n
}

// kernCount runs `kern <sub> <file>` and returns len(result.<field>) or "n/a".
func kernCount(root, kernBin, sub, file, field string) string {
	if kernBin == "" {
		return "n/a"
	}
	cmd := exec.Command(kernBin, sub, file)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "n/a"
	}
	var resp struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.Result == nil {
		return "n/a"
	}
	var items []json.RawMessage
	if err := json.Unmarshal(resp.Result[field], &items); err != nil {
		return "n/a"
	}
	return strconv.Itoa(len(items))
}

// aliasingExperiment builds the kern vector program from its emitted LLVM
// text twice, once as emitted and once with `noalias` and `readonly`
// stripped, and reports both throughputs.
func aliasingExperiment(root, buildDir, kernBin string, runs int, rows []*row) string {
	var b strings.Builder
	b.WriteString("## [unproven] aliasing experiment (vector)\n\n")
	src := filepath.Join(root, "bench", "kern", "vector", "vector.kern")
	if kernBin == "" || !exists(src) {
		b.WriteString("Skipped: bench/kern/vector/vector.kern or the kern driver is unavailable.\n\n")
		return b.String()
	}
	dir := filepath.Join(buildDir, "vector")
	ll := filepath.Join(dir, "kern-release.ll")
	if msg, err := runCmd(root, kernBin, "build", "--opt=release", "--emit-llvm="+ll, "-o", filepath.Join(dir, "kern-emit"), src); err != nil {
		fmt.Fprintf(&b, "Skipped: kern build --emit-llvm failed: %v: %s\n\n", err, firstLine(msg))
		return b.String()
	}
	text, err := os.ReadFile(ll)
	if err != nil {
		fmt.Fprintf(&b, "Skipped: %v\n\n", err)
		return b.String()
	}
	attrRE := regexp.MustCompile(`\b(noalias|readonly)\b`)
	removed := len(attrRE.FindAll(text, -1))
	stripped := filepath.Join(dir, "kern-release-noattr.ll")
	if err := os.WriteFile(stripped, attrRE.ReplaceAll(text, nil), 0o644); err != nil {
		fmt.Fprintf(&b, "Skipped: %v\n\n", err)
		return b.String()
	}
	rt := filepath.Join(root, "runtime", "hosted", "src", "kern_rt.c")
	variants := []struct{ name, ll, bin string }{
		{"with attributes (as emitted)", ll, filepath.Join(dir, "kern-attr")},
		{"attributes stripped", stripped, filepath.Join(dir, "kern-noattr")},
	}
	fmt.Fprintf(&b, "Both binaries are produced by `clang -O2` from the LLVM text that `kern build --opt=release --emit-llvm` wrote, linked with runtime/hosted/src/kern_rt.c; the second has every `noalias` and `readonly` token removed (%d tokens).\n\n", removed)
	b.WriteString("| variant | throughput (Mops/s) | p50 ns | p99 ns |\n|---|---|---|---|\n")
	for _, v := range variants {
		if msg, err := runCmd(root, "clang", "-O2", "-Wno-override-module", "-ffp-model=strict", "-DKERN_RELEASE", "-o", v.bin, v.ll, rt, "-lpthread"); err != nil {
			fmt.Fprintf(&b, "| %s | build failed: %s | | |\n", v.name, firstLine(msg))
			continue
		}
		var rs []runResult
		for i := 0; i < runs; i++ {
			res, err := runOnce(root, v.bin, nil)
			if err != nil {
				fmt.Fprintf(&b, "| %s | run failed: %v | | |\n", v.name, err)
				rs = nil
				break
			}
			rs = append(rs, res)
		}
		if len(rs) == 0 {
			continue
		}
		tp, tpCI := meanCI(mapRuns(rs, func(r runResult) float64 { return float64(r.ops) / float64(r.ns) * 1e3 }))
		p50, p50CI := meanCI(mapRuns(rs, func(r runResult) float64 { return float64(r.p50) }))
		p99, p99CI := meanCI(mapRuns(rs, func(r runResult) float64 { return float64(r.p99) }))
		fmt.Fprintf(&b, "| %s | %.1f ± %.1f | %.0f ± %.0f | %.0f ± %.0f |\n", v.name, tp, tpCI, p50, p50CI, p99, p99CI)
	}
	for _, r := range rows {
		if r.workload == "vector" && r.lang == "rust" && len(r.runs) > 0 {
			tp, ci := meanCI(mapRuns(r.runs, func(x runResult) float64 { return float64(x.ops) / float64(x.ns) * 1e3 }))
			fmt.Fprintf(&b, "| Rust `&mut`/`&` version (context) | %.1f ± %.1f | | |\n", tp, ci)
		}
	}
	b.WriteString("\nThe result is marked [unproven]: it measures whether LLVM uses the aliasing attributes on this workload and nothing more.\n\n")
	return b.String()
}

// writeReport renders the Markdown report.
func writeReport(root string, tc toolchain, workloads []string, rows []*row, runs int, experiment string) string {
	var b strings.Builder
	b.WriteString("# Benchmark report\n\n")
	b.WriteString("Generated by `go run ./bench/harness` (bench/PROTOCOL.md). Do not edit by hand.\n\n")
	b.WriteString("## Host and toolchain\n\n")
	fmt.Fprintf(&b, "- date: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "- host: %s\n", hostDescription())
	fmt.Fprintf(&b, "- clang: %s\n- rustc: %s\n- cargo: %s\n- go: %s\n", tc.clang, tc.rustc, tc.cargo, tc.goVer)
	fmt.Fprintf(&b, "- runs per row: %d; intervals are 95%% confidence (Student's t, n-1 degrees of freedom)\n", runs)
	b.WriteString("- the kern driver is built once with `go build ./cmd/kern`; kern compile times are for `kern build` alone\n")
	b.WriteString("- the reserved extension column is always \"no\" in Phase 2; only the user-level row (§2.3(a)) exists per workload in this phase\n")
	b.WriteString("- threads: the workload's thread count (workers + the main thread where it feeds work); failure path: the failures= count printed by the KERN_BENCH_FAIL=1 run, n/a when the workload has no failure path or the program prints no failures field\n")
	if la := loadAverage(); la != "" {
		fmt.Fprintf(&b, "- load average at start (1, 5, 15 min): %s; multi-threaded rows are sensitive to other load on the host\n", la)
	}
	for _, w := range tc.warnings {
		fmt.Fprintf(&b, "- warning: %s\n", w)
	}
	b.WriteString("\n")

	for _, w := range workloads {
		fmt.Fprintf(&b, "## %s\n\n", w)
		var wrows []*row
		for _, r := range rows {
			if r.workload == w {
				wrows = append(wrows, r)
			}
		}
		b.WriteString(checksumLine(wrows))
		b.WriteString(failureLine(w, wrows))
		b.WriteString(targetLine(wrows))
		for _, r := range wrows {
			for _, x := range r.runs {
				if x.wall > slowRun {
					fmt.Fprintf(&b, "Note: %s/%s took %.1f s in one run, above the ~5 s guideline; the ops count was left as specified.\n\n", r.lang, r.cfg, x.wall.Seconds())
					break
				}
			}
		}
		for _, cfg := range []string{"release", "debug"} {
			var crows []*row
			for _, r := range wrows {
				if r.cfg == cfg {
					crows = append(crows, r)
				}
			}
			if len(crows) == 0 {
				continue
			}
			fmt.Fprintf(&b, "### %s\n\n", cfg)
			b.WriteString("| language | threads | throughput (Mops/s) | p50 ns | p99 ns | peak RSS (MiB) | text bytes | compile time | unsafe lines | allocs | copies | ownership places | reserved ext | failure path | checksum |\n")
			b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
			var notes []string
			threads := threadsOf(w)
			for _, r := range crows {
				switch {
				case r.skip != "":
					fmt.Fprintf(&b, "| %s | %s | skipped | | | | | | | | | | | | |\n", r.label(), threads)
					notes = append(notes, fmt.Sprintf("%s: skipped (%s)", r.label(), r.skip))
				case r.buildErr != "":
					fmt.Fprintf(&b, "| %s | %s | build failed | | | | | | | | | | | | |\n", r.label(), threads)
					notes = append(notes, fmt.Sprintf("%s: build failed: %s", r.label(), firstLine(r.buildErr)))
				case len(r.runs) == 0:
					fmt.Fprintf(&b, "| %s | %s | run failed | | | | | %s | %d | %s | %s | %s | no | %s | |\n", r.label(), threads, r.compile.Round(time.Millisecond), r.unsafeLines, r.allocs, r.copies, r.places, failureCell(r))
					notes = append(notes, fmt.Sprintf("%s: every run failed: %s", r.label(), firstLine(strings.Join(r.errs, "; "))))
				default:
					tp, tpCI := meanCI(mapRuns(r.runs, func(x runResult) float64 { return float64(x.ops) / float64(x.ns) * 1e3 }))
					p50, p50CI := meanCI(mapRuns(r.runs, func(x runResult) float64 { return float64(x.p50) }))
					p99, p99CI := meanCI(mapRuns(r.runs, func(x runResult) float64 { return float64(x.p99) }))
					rss, rssCI := meanCI(mapRuns(r.runs, func(x runResult) float64 { return x.rss / (1 << 20) }))
					text := strconv.FormatInt(r.text, 10)
					if r.textNote != "" {
						text += " (" + r.textNote + ")"
					}
					fmt.Fprintf(&b, "| %s | %s | %.1f ± %.1f | %.0f ± %.0f | %.0f ± %.0f | %.1f ± %.1f | %s | %s | %d | %s | %s | %s | no | %s | %d |\n",
						r.label(), threads, tp, tpCI, p50, p50CI, p99, p99CI, rss, rssCI, text, r.compile.Round(time.Millisecond), r.unsafeLines, r.allocs, r.copies, r.places, failureCell(r), r.runs[0].checksum)
					if len(r.errs) > 0 {
						notes = append(notes, fmt.Sprintf("%s: %d of %d runs failed: %s", r.label(), len(r.errs), runs, firstLine(r.errs[0])))
					}
				}
				if r.failErr != "" {
					notes = append(notes, fmt.Sprintf("%s: failure-path run failed: %s", r.label(), firstLine(r.failErr)))
				}
			}
			b.WriteString("\n")
			for _, n := range notes {
				fmt.Fprintf(&b, "- %s\n", n)
			}
			if len(notes) > 0 {
				b.WriteString("\n")
			}
		}
	}
	if experiment != "" {
		b.WriteString(experiment)
	}
	_ = root
	return b.String()
}

// failureCell renders the "failure path" column of a row.
func failureCell(r *row) string {
	if r.failRun == nil {
		return "n/a"
	}
	return strconv.FormatUint(r.failRun.failures, 10)
}

// failureLine states whether the failure-path runs of a workload agree on
// their failures count and checksum.
func failureLine(workload string, rows []*row) string {
	if !failureWorkloads[workload] {
		return ""
	}
	var measured []*row
	for _, r := range rows {
		if r.failRun != nil {
			measured = append(measured, r)
		}
	}
	if len(measured) == 0 {
		return "Failure path: no row printed a failures field.\n\n"
	}
	first := measured[0].failRun
	agree := true
	for _, r := range measured[1:] {
		if r.failRun.failures != first.failures || r.failRun.checksum != first.checksum {
			agree = false
		}
	}
	if agree {
		return fmt.Sprintf("Failure path: agree across %d row(s) (failures=%d, checksum=%d).\n\n", len(measured), first.failures, first.checksum)
	}
	var b strings.Builder
	b.WriteString("Failure path: **MISMATCH**:")
	for _, r := range measured {
		fmt.Fprintf(&b, " %s=failures:%d/checksum:%d", r.key(), r.failRun.failures, r.failRun.checksum)
	}
	b.WriteString("\n\n")
	return b.String()
}

// loadAverage returns the host's 1/5/15-minute load averages, or "".
func loadAverage() string {
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
			return strings.Trim(strings.TrimSpace(string(out)), "{} ")
		}
	case "linux":
		if data, err := os.ReadFile("/proc/loadavg"); err == nil {
			if f := strings.Fields(string(data)); len(f) >= 3 {
				return strings.Join(f[:3], " ")
			}
		}
	}
	return ""
}

// checksumLine states whether every measured row of a workload agrees.
func checksumLine(rows []*row) string {
	type cs struct {
		key string
		val uint64
	}
	var seen []cs
	var problems []string
	for _, r := range rows {
		if len(r.runs) == 0 || r.isFailureTwin() {
			continue // a failure-path twin program is compared by failureLine
		}
		first := r.runs[0].checksum
		for _, x := range r.runs[1:] {
			if x.checksum != first {
				problems = append(problems, fmt.Sprintf("%s is not deterministic across runs (%d vs %d)", r.key(), first, x.checksum))
				break
			}
		}
		seen = append(seen, cs{r.key(), first})
	}
	if len(seen) == 0 {
		return "Checksums: no row produced a result.\n\n"
	}
	agree := true
	for _, c := range seen[1:] {
		if c.val != seen[0].val {
			agree = false
		}
	}
	var b strings.Builder
	if agree && len(problems) == 0 {
		fmt.Fprintf(&b, "Checksums: agree across %d row(s) (%d).\n\n", len(seen), seen[0].val)
		return b.String()
	}
	b.WriteString("Checksums: **MISMATCH**:")
	for _, c := range seen {
		fmt.Fprintf(&b, " %s=%d", c.key, c.val)
	}
	for _, p := range problems {
		fmt.Fprintf(&b, "; %s", p)
	}
	b.WriteString("\n\n")
	return b.String()
}

// targetLine compares kern release against the better of C and Rust on
// throughput (within 10% below) and peak RSS (within 10% above).
func targetLine(rows []*row) string {
	var kern *row
	bestTP, bestTPLang := 0.0, ""
	bestRSS, bestRSSLang := math.Inf(1), ""
	for _, r := range rows {
		if len(r.runs) == 0 {
			continue
		}
		tp, _ := meanCI(mapRuns(r.runs, func(x runResult) float64 { return float64(x.ops) / float64(x.ns) * 1e3 }))
		rss, _ := meanCI(mapRuns(r.runs, func(x runResult) float64 { return x.rss / (1 << 20) }))
		switch {
		case r.lang == "kern" && r.cfg == "release" && r.isMain():
			kern = r // the workload's main program by name
		case r.lang == "c" || r.lang == "rust":
			if tp > bestTP {
				bestTP, bestTPLang = tp, r.lang
			}
			if rss < bestRSS {
				bestRSS, bestRSSLang = rss, r.lang
			}
		}
	}
	if bestTPLang == "" {
		return "Targets: no C or Rust baseline was measured.\n\n"
	}
	if kern == nil {
		return fmt.Sprintf("Targets: kern release row not measured; best C/Rust throughput %.1f Mops/s (%s), lowest peak RSS %.1f MiB (%s).\n\n", bestTP, bestTPLang, bestRSS, bestRSSLang)
	}
	tp, _ := meanCI(mapRuns(kern.runs, func(x runResult) float64 { return float64(x.ops) / float64(x.ns) * 1e3 }))
	rss, _ := meanCI(mapRuns(kern.runs, func(x runResult) float64 { return x.rss / (1 << 20) }))
	verdict := func(ok bool) string {
		if ok {
			return "meets"
		}
		return "does not meet"
	}
	return fmt.Sprintf("Targets: kern release throughput %.1f Mops/s vs best %.1f (%s), ratio %.2f, %s the 10%% target; peak RSS %.1f MiB vs lowest %.1f (%s), ratio %.2f, %s the 10%% target.\n\n",
		tp, bestTP, bestTPLang, tp/bestTP, verdict(tp >= 0.9*bestTP), rss, bestRSS, bestRSSLang, rss/bestRSS, verdict(rss <= 1.1*bestRSS))
}

func summary(r *row) string {
	tp, ci := meanCI(mapRuns(r.runs, func(x runResult) float64 { return float64(x.ops) / float64(x.ns) * 1e3 }))
	rss, _ := meanCI(mapRuns(r.runs, func(x runResult) float64 { return x.rss / (1 << 20) }))
	return fmt.Sprintf("%.1f ± %.1f Mops/s, rss %.1f MiB, checksum %d, compile %s", tp, ci, rss, r.runs[0].checksum, r.compile.Round(time.Millisecond))
}

// tTable holds two-sided 95% Student's t critical values by degrees of freedom.
var tTable = []float64{0, 12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
	2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
	2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042}

// meanCI returns the sample mean and the half-width of its 95% confidence
// interval; the interval is 0 for a single sample.
func meanCI(xs []float64) (float64, float64) {
	n := len(xs)
	if n == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(n)
	if n < 2 {
		return mean, 0
	}
	ss := 0.0
	for _, x := range xs {
		ss += (x - mean) * (x - mean)
	}
	sd := math.Sqrt(ss / float64(n-1))
	df := n - 1
	t := 1.960
	if df < len(tTable) {
		t = tTable[df]
	}
	return mean, t * sd / math.Sqrt(float64(n))
}

func mapRuns(rs []runResult, f func(runResult) float64) []float64 {
	out := make([]float64, len(rs))
	for i, r := range rs {
		out[i] = f(r)
	}
	return out
}

// readToolchain records the installed tool versions and compares their
// major versions with bench/toolchain.lock.
func readToolchain(root string) toolchain {
	ver := func(re string, name string, args ...string) string {
		out, err := exec.Command(name, args...).Output()
		if err != nil {
			return "not found"
		}
		if m := regexp.MustCompile(re).FindStringSubmatch(string(out)); m != nil {
			return m[1]
		}
		return firstLine(string(out))
	}
	tc := toolchain{
		clang: ver(`clang version (\d+\.\d+\.\d+)`, "clang", "--version"),
		rustc: ver(`rustc (\d+\.\d+\.\d+)`, "rustc", "--version"),
		cargo: ver(`cargo (\d+\.\d+\.\d+)`, "cargo", "--version"),
		goVer: ver(`go(\d+\.\d+(?:\.\d+)?)`, "go", "version"),
	}
	data, err := os.ReadFile(filepath.Join(root, "bench", "toolchain.lock"))
	if err != nil {
		tc.warnings = append(tc.warnings, "bench/toolchain.lock not readable: "+err.Error())
		return tc
	}
	installed := map[string]string{"clang": tc.clang, "rustc": tc.rustc, "cargo": tc.cargo, "go": tc.goVer}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		have, known := installed[strings.TrimSpace(k)]
		if !known {
			continue
		}
		want := strings.TrimSpace(v)
		if major(have) != major(want) {
			tc.warnings = append(tc.warnings, fmt.Sprintf("%s major version differs from bench/toolchain.lock: installed %s, locked %s", k, have, want))
		} else if have != want {
			tc.warnings = append(tc.warnings, fmt.Sprintf("%s version differs from bench/toolchain.lock (same major): installed %s, locked %s", k, have, want))
		}
	}
	return tc
}

func major(v string) string {
	m, _, _ := strings.Cut(v, ".")
	return m
}

func hostDescription() string {
	host, _ := os.Hostname()
	cpu := ""
	switch runtime.GOOS {
	case "darwin":
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			cpu = strings.TrimSpace(string(out))
		}
	case "linux":
		if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "model name") {
					_, cpu, _ = strings.Cut(line, ":")
					cpu = strings.TrimSpace(cpu)
					break
				}
			}
		}
	}
	uname := ""
	if out, err := exec.Command("uname", "-srm").Output(); err == nil {
		uname = strings.TrimSpace(string(out))
	}
	return fmt.Sprintf("%s, %s/%s, %s, %s, %d logical CPUs", host, runtime.GOOS, runtime.GOARCH, uname, cpu, runtime.NumCPU())
}

// findRoot locates the repository root (the directory holding go.mod and
// bench/) starting from the working directory.
func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if exists(filepath.Join(dir, "go.mod")) && exists(filepath.Join(dir, "bench")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run from inside the kern repository (go.mod and bench/ not found)")
		}
		dir = parent
	}
}

func runCmd(dir, name string, args ...string) (string, error) {
	return runCmdEnv(dir, nil, name, args...)
}

// runCmdEnv runs a command and returns its combined output.
func runCmdEnv(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func globFiles(dir string, patterns ...string) []string {
	var files []string
	for _, p := range patterns {
		m, _ := filepath.Glob(filepath.Join(dir, p))
		files = append(files, m...)
	}
	sort.Strings(files)
	return files
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func logf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func fatalf(format string, args ...any) {
	logf("harness: "+format, args...)
	os.Exit(2)
}
