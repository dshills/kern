// Command agent runs the §9 agent experiment (PLAN.md 4.2): for every task
// under bench/agent/tasks, each model and each arm, it drives an LLM agent
// through a tool loop against the kern compiler, scores the trial by the
// task's hidden tests within the iteration and time budgets, and records
// every tool call so the counts are auditable.
//
//	go run ./bench/agent/harness [-tasks t1,t2] [-models openai:gpt-6.1-sol,anthropic:claude-opus-5-5]
//	    [-arms structured,plain] [-trials 5] [-iterations 20] [-budget 30m] [-effort max] [-out bench/agent/REPORT.md]
//
// Model access uses OPENAI_API_KEY, ANTHROPIC_API_KEY, and WAFER_API_KEY
// with WAFER_API_URL for wafer:<model>; -effort sets the reasoning effort. Trials are
// recorded under bench/agent/runs/<timestamp>/ as JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type task struct {
	Name        string
	Dir         string
	Prompt      string
	Kind        string   // implementation | debugging | refactoring | optimization | review
	Files       []string // starting source files (relative to Dir/start)
	Natural     string   // Dir/calibration/natural when the task has a natural solution (specs/agent-suite-v4 R5), else ""
	Hidden      hiddenSpec
	Description string
}

// hiddenSpec is the task's hidden test suite: expected program output,
// and optional constraints checked through the machine API.
type hiddenSpec struct {
	Main           string         `json:"main"`             // the file with main (relative)
	ExpectedOutput string         `json:"expected_output"`  // exact stdout of `kern run`
	MaxAllocations *int           `json:"max_allocations"`  // show-allocations sites, when set
	MaxCopies      *int           `json:"max_copies"`       // show-copies entries, when set
	NoUnsafeAdded  bool           `json:"no_unsafe_added"`  // the agent must not add unsafe blocks
	MustCheckClean bool           `json:"must_check_clean"` // check must report no diagnostics
	ExtraPrograms  []extraProgram `json:"extra_programs"`   // additional hidden programs compiled with the solution

	// MaxRuntimeBoundsChecks bounds the show-bounds-checks entries of kind
	// bounds that are runtime-checked (not proven), when set.
	MaxRuntimeBoundsChecks *int `json:"max_runtime_bounds_checks"`
	// MaxTypeSize bounds the show-layout size of named types.
	MaxTypeSize []typeSize `json:"max_type_size"`
	// RequiredFunctions must each be a function of the program, named as
	// show-effects names them (module.f, Type.method), so that a solution
	// cannot delete what the task asks to keep.
	RequiredFunctions []string `json:"required_functions"`
	// MustSpawn names functions that show-effects must report as spawning
	// threads (`spawns` true), so that a parallel task cannot be solved
	// sequentially.
	MustSpawn []string `json:"must_spawn"`
	// FrozenFunctions are top-level functions whose source text must equal
	// the starting sources' (specs/agent-suite-v4 R10): the given driver.
	FrozenFunctions []string `json:"frozen_functions"`
}

type typeSize struct {
	Type  string `json:"type"`
	Bytes int    `json:"bytes"`
}

type extraProgram struct {
	File           string `json:"file"`            // hidden main file placed next to the solution
	ExpectedOutput string `json:"expected_output"` // its stdout
}

type trialRecord struct {
	Task        string        `json:"task"`
	Model       string        `json:"model"`
	Arm         string        `json:"arm"`
	Trial       int           `json:"trial"`
	Success     bool          `json:"success"`
	Iterations  int           `json:"iterations"`
	WallSeconds float64       `json:"wall_seconds"`
	InputTokens int           `json:"input_tokens"`
	OutputTok   int           `json:"output_tokens"`
	Regressions int           `json:"regressions"`
	UnsafeAdded int           `json:"unsafe_lines_added"`
	TimedOut    bool          `json:"timed_out"`
	Error       string        `json:"error,omitempty"`
	Calls       []toolCallLog `json:"tool_calls"`
	// What the trial actually did, kept apart from the scored Iterations
	// and WallSeconds, which a failed trial sets to the budget maximum:
	// why it ended ("done", "iterations", "timeout" or "error"), the
	// iterations and seconds it used, and the model's text replies.
	EndReason      string     `json:"end_reason,omitempty"`
	UsedIterations int        `json:"used_iterations,omitempty"`
	UsedSeconds    float64    `json:"used_seconds,omitempty"`
	Replies        []replyLog `json:"replies,omitempty"`
	// StepSeconds is how long each model call took, retries included.
	StepSeconds []float64 `json:"step_seconds,omitempty"`
}

type replyLog struct {
	Iteration int    `json:"iteration"`
	Text      string `json:"text"`
}

type toolCallLog struct {
	Iteration int    `json:"iteration"`
	Tool      string `json:"tool"`
	Args      string `json:"args"`
	Result    string `json:"result"`
}

func main() {
	tasksFlag := flag.String("tasks", "", "comma-separated task names (default: all)")
	tasksDir := flag.String("tasks-dir", "bench/agent/tasks", "task directory, relative to the repository root")
	modelsFlag := flag.String("models", "openai:gpt-6.1-sol,anthropic:claude-opus-5-5", "provider:model pairs")
	armsFlag := flag.String("arms", "structured,plain", "arms to run, of structured, queries, documented and plain")
	trials := flag.Int("trials", 5, "trials per task, model and arm")
	iterations := flag.Int("iterations", 20, "iteration budget per trial")
	budget := flag.Duration("budget", 30*time.Minute, "wall-clock budget per trial")
	out := flag.String("out", "", "report path (default bench/agent/REPORT.md)")
	dryRun := flag.Bool("dry-run", false, "validate tasks (hidden tests against the reference solutions) without calling any model")
	kernFlag := flag.String("kern", "", "a prebuilt kern binary to use instead of building one into bench/.build (lets several dry runs share one build)")
	reportFrom := flag.String("report-from", "", "score the trial records of existing run directories (comma-separated) into one report without calling any model (budgets are taken from the flags)")
	redoErrors := flag.String("redo-errors", "", "re-run, into this existing run directory, every trial whose model call failed (end_reason error), keeping the failed record beside it as <name>.json.error, then report the directory")
	callTimeout := flag.Duration("call-timeout", 180*time.Second, "limit on one model call, response included (calls are not streamed, so a long reasoning turn needs a higher limit)")
	parallel := flag.Int("parallel", 1, "trials run at once; each has its own workspace and budget, so only the wall-clock column (reported, not compared) can change")
	effort := flag.String("effort", "", "reasoning effort sent to openai and wafer models (for example high or max); the records name the model as provider:model@effort")
	flag.Parse()
	root, err := findRoot()
	if err != nil {
		fatalf("%v", err)
	}
	kernBin := filepath.Join(root, "bench", ".build", "kern")
	if *kernFlag != "" {
		abs, err := filepath.Abs(*kernFlag)
		if err != nil {
			fatalf("-kern: %v", err)
		}
		kernBin = abs
	} else if msg, err := runCmd(root, "go", "build", "-o", kernBin, "./cmd/kern"); err != nil {
		fatalf("building kern: %v: %s", err, msg)
	}
	tasks := loadTasks(filepath.Join(root, *tasksDir))
	if *tasksFlag != "" {
		want := map[string]bool{}
		for _, t := range strings.Split(*tasksFlag, ",") {
			want[t] = true
		}
		var sel []*task
		for _, t := range tasks {
			if want[t.Name] {
				sel = append(sel, t)
			}
		}
		tasks = sel
	}
	if *dryRun {
		failed := false
		for _, t := range tasks {
			ok, detail := validateReference(context.Background(), root, kernBin, t)
			fmt.Printf("%s: reference solution %s %s\n", t.Name, map[bool]string{true: "passes", false: "FAILS"}[ok], detail)
			fmt.Printf("%s: reference measures %s\n", t.Name, measure(context.Background(), kernBin, t, filepath.Join(t.Dir, "reference")))
			fmt.Printf("%s: start measures %s\n", t.Name, measure(context.Background(), kernBin, t, filepath.Join(t.Dir, "start")))
			bad, detail2 := hiddenPasses(context.Background(), root, kernBin, t, filepath.Join(t.Dir, "start"))
			fmt.Printf("%s: starting sources %s hidden tests %s\n", t.Name, map[bool]string{true: "already pass (task is trivial!)", false: "fail as intended"}[bad], detail2)
			if msg := promptStatesBudget(t); msg != "" {
				fmt.Printf("%s: %s\n", t.Name, msg)
				failed = true
			}
			if !ok || bad {
				failed = true
			}
			if t.Natural != "" && !calibrate(context.Background(), root, kernBin, t) {
				failed = true
			}
		}
		if failed {
			os.Exit(1)
		}
		return
	}
	if *reportFrom != "" {
		records, n := loadRuns(strings.Split(*reportFrom, ","), tasks)
		var rels []string
		for _, d := range strings.Split(*reportFrom, ",") {
			rels = append(rels, relRun(root, strings.TrimSpace(d)))
		}
		writeReport(root, *out, writeAgentReport(records, tasks, n, *iterations, *budget, strings.Join(rels, "`, `")), len(records), *reportFrom)
		return
	}
	httpClient.Timeout = *callTimeout
	if *redoErrors != "" {
		redo(root, kernBin, *redoErrors, tasks, *parallel, *iterations, *budget, *out)
		return
	}
	for _, arm := range strings.Split(*armsFlag, ",") {
		known := false
		for _, a := range arms {
			known = known || a == arm
		}
		if !known {
			fatalf("-arms: unknown arm %q (%s)", arm, strings.Join(arms, ", "))
		}
	}
	runDir := filepath.Join(root, "bench", "agent", "runs", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	var jobs []job
	for _, ms := range strings.Split(*modelsFlag, ",") {
		provider, model, _ := strings.Cut(ms, ":")
		client, err := newClient(provider, model, *effort)
		if err != nil {
			fatalf("%v", err)
		}
		if *effort != "" {
			ms += "@" + *effort
		}
		// trial-major order, so that a run stopped early has every arm and
		// task at the same trial count
		for trial := 1; trial <= *trials; trial++ {
			for _, t := range tasks {
				for _, arm := range strings.Split(*armsFlag, ",") {
					jobs = append(jobs, job{client, ms, arm, t, trial})
				}
			}
		}
	}
	records := runJobs(root, kernBin, runDir, jobs, *parallel, *iterations, *budget)
	writeReport(root, *out, writeAgentReport(records, tasks, *trials, *iterations, *budget, relRun(root, runDir)), len(records), runDir)
}

// job is one trial to run.
type job struct {
	client llmClient
	model  string // provider:model, with @effort when one was set
	arm    string
	t      *task
	trial  int
}

// recordName is a trial record's file name in its run directory.
func recordName(task, model, arm string, trial int) string {
	return fmt.Sprintf("%s_%s_%s_%d.json", task, strings.ReplaceAll(model, ":", "-"), arm, trial)
}

// runJobs runs the trials parallel at a time and records each in runDir.
func runJobs(root, kernBin, runDir string, jobs []job, parallel, iterations int, budget time.Duration) []trialRecord {
	var (
		mu      sync.Mutex
		records []trialRecord
		wg      sync.WaitGroup
	)
	next := make(chan job)
	for w := 0; w < max(1, parallel); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range next {
				logf("task %s model %s arm %s trial %d", j.t.Name, j.model, j.arm, j.trial)
				rec := runTrial(root, kernBin, runDir, j.client, j.model, j.arm, j.t, j.trial, iterations, budget)
				b, _ := json.MarshalIndent(rec, "", " ")
				if err := os.WriteFile(filepath.Join(runDir, recordName(j.t.Name, j.model, j.arm, j.trial)), b, 0o644); err != nil {
					fatalf("recording trial: %v", err)
				}
				logf("  -> %s %s %s %d: success=%v end=%s used %d iterations %.0fs tokens=%d/%d", j.t.Name, j.model, j.arm, j.trial, rec.Success, rec.EndReason, rec.UsedIterations, rec.UsedSeconds, rec.InputTokens, rec.OutputTok)
				mu.Lock()
				records = append(records, rec)
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		next <- j
	}
	close(next)
	wg.Wait()
	return records
}

// redo re-runs the trials of runDir whose model call failed: an API
// failure says nothing about the agent, so the trial is run again under
// the same task, model, effort, arm and trial number. The failed record is
// kept as <name>.json.error (.error2 and so on for a later redo of the
// same trial), which the report does not read.
func redo(root, kernBin, runDir string, tasks []*task, parallel, iterations int, budget time.Duration, out string) {
	records, n := loadRecords(runDir, tasks)
	byName := map[string]*task{}
	for _, t := range tasks {
		byName[t.Name] = t
	}
	clients := map[string]llmClient{}
	var jobs []job
	for _, r := range records {
		if r.EndReason != "error" {
			continue
		}
		c := clients[r.Model]
		if c == nil {
			pm, effort, _ := strings.Cut(r.Model, "@")
			provider, model, _ := strings.Cut(pm, ":")
			var err error
			if c, err = newClient(provider, model, effort); err != nil {
				fatalf("%v", err)
			}
			clients[r.Model] = c
		}
		jobs = append(jobs, job{c, r.Model, r.Arm, byName[r.Task], r.Trial})
	}
	// every client exists before any record moves, so a failure above
	// leaves the directory as it was
	for _, j := range jobs {
		name := filepath.Join(runDir, recordName(j.t.Name, j.model, j.arm, j.trial))
		kept := name + ".error"
		for k := 2; ; k++ {
			if _, err := os.Stat(kept); os.IsNotExist(err) {
				break
			}
			kept = fmt.Sprintf("%s.error%d", name, k)
		}
		if err := os.Rename(name, kept); err != nil {
			fatalf("keeping the failed record: %v", err)
		}
	}
	logf("re-running %d trials whose model call failed", len(jobs))
	runJobs(root, kernBin, runDir, jobs, parallel, iterations, budget)
	records, _ = loadRecords(runDir, tasks)
	writeReport(root, out, writeAgentReport(records, tasks, n, iterations, budget, relRun(root, runDir)), len(records), runDir)
}

// relRun names a run directory relative to the repository root for the
// report, falling back to the path as given.
func relRun(root, dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return dir
}

// writeReport writes the report to path (default bench/agent/REPORT.md).
func writeReport(root, path, report string, n int, runDir string) {
	if path == "" {
		path = filepath.Join(root, "bench", "agent", "REPORT.md")
	}
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("wrote %s (%d trials under %s)\n", path, n, runDir)
}

// loadRecords reads every trial record under a run directory and returns
// them with the highest trial number seen, which is the trials-per-cell the
// run was made with. A record for a task that is not in the task list is an
// error, since the report would silently leave it out.
func loadRecords(dir string, tasks []*task) ([]trialRecord, int) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		fatalf("no trial records under %s", dir)
	}
	known := map[string]bool{}
	for _, t := range tasks {
		known[t.Name] = true
	}
	var records []trialRecord
	n := 0
	seen := map[string]string{} // task|model|arm|trial -> file, so a copied record cannot count twice
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			fatalf("%v", err)
		}
		var rec trialRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			fatalf("%s: %v", f, err)
		}
		if !known[rec.Task] {
			fatalf("%s: task %s is not under the task directory", f, rec.Task)
		}
		if rec.Trial <= 0 {
			fatalf("%s: trial number %d is not positive", f, rec.Trial)
		}
		known := false
		for _, a := range arms {
			known = known || a == rec.Arm
		}
		if !known {
			fatalf("%s: arm %q is not one the report scores", f, rec.Arm)
		}
		id := trialID(rec)
		if prev, dup := seen[id]; dup {
			fatalf("%s and %s record the same trial (%s)", prev, f, id)
		}
		seen[id] = f
		if rec.Trial > n {
			n = rec.Trial
		}
		records = append(records, rec)
	}
	return records, n
}

// promptStatesBudget checks that a task's prompt states the bounds-check
// budget its hidden suite enforces, "" when it does. Twice a budget was
// lowered in hidden.json and not in the prompt (suites 2 and 3, f15f773),
// so an agent met the budget it was told and failed the one it was not.
func promptStatesBudget(t *task) string {
	if t.Hidden.MaxRuntimeBoundsChecks == nil {
		return ""
	}
	n := *t.Hidden.MaxRuntimeBoundsChecks
	want := fmt.Sprintf("at most %d runtime-checked bounds check", n)
	if n == 0 {
		want = "no runtime-checked bounds checks"
	}
	if !strings.Contains(t.Prompt, want) {
		return fmt.Sprintf("the prompt does not state the hidden bounds-check budget (%q)", want)
	}
	return ""
}

// trialID names a trial across run directories.
func trialID(r trialRecord) string {
	return fmt.Sprintf("%s|%s|%s|%d", r.Task, r.Model, r.Arm, r.Trial)
}

// loadRuns reads the records of several run directories, as loadRecords
// does one; a trial recorded in two directories is an error.
func loadRuns(dirs []string, tasks []*task) ([]trialRecord, int) {
	var all []trialRecord
	n := 0
	seen := map[string]string{}
	for _, d := range dirs {
		recs, k := loadRecords(strings.TrimSpace(d), tasks)
		if n != 0 && k != n {
			fatalf("%s records %d trials per cell, but earlier run directories record %d", d, k, n)
		}
		for _, r := range recs {
			id := trialID(r)
			if prev, dup := seen[id]; dup {
				fatalf("%s and %s both record the trial %s", prev, d, id)
			}
			seen[id] = d
		}
		all = append(all, recs...)
		n = max(n, k)
	}
	return all, n
}

// loadTasks reads <dir>/<name>/{task.md,hidden.json,start/,reference/}.
func loadTasks(dir string) []*task {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fatalf("%v", err)
	}
	var tasks []*task
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t := &task{Name: e.Name(), Dir: filepath.Join(dir, e.Name())}
		prompt, err := os.ReadFile(filepath.Join(t.Dir, "task.md"))
		if err != nil {
			fatalf("%s: %v", t.Name, err)
		}
		t.Prompt = string(prompt)
		for _, line := range strings.Split(t.Prompt, "\n") {
			if strings.HasPrefix(line, "Kind: ") {
				t.Kind = strings.TrimSpace(strings.TrimPrefix(line, "Kind: "))
			}
		}
		hb, err := os.ReadFile(filepath.Join(t.Dir, "hidden.json"))
		if err != nil {
			fatalf("%s: %v", t.Name, err)
		}
		if err := json.Unmarshal(hb, &t.Hidden); err != nil {
			fatalf("%s: hidden.json: %v", t.Name, err)
		}
		m, _ := filepath.Glob(filepath.Join(t.Dir, "start", "*.kern"))
		for _, f := range m {
			t.Files = append(t.Files, filepath.Base(f))
		}
		sort.Strings(t.Files)
		if st, err := os.Stat(filepath.Join(t.Dir, "calibration", "natural")); err == nil && st.IsDir() {
			t.Natural = filepath.Join(t.Dir, "calibration", "natural")
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	return tasks
}

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
func fatalf(format string, args ...any) {
	logf(format, args...)
	os.Exit(1)
}
