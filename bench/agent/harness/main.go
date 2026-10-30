// Command agent runs the §9 agent experiment (PLAN.md 4.2): for every task
// under bench/agent/tasks, each model and each arm, it drives an LLM agent
// through a tool loop against the kern compiler, scores the trial by the
// task's hidden tests within the iteration and time budgets, and records
// every tool call so the counts are auditable.
//
//	go run ./bench/agent/harness [-tasks t1,t2] [-models openai:gpt-6.1-sol,anthropic:claude-opus-5-5]
//	    [-arms structured,plain] [-trials 5] [-iterations 20] [-budget 30m] [-out bench/agent/REPORT.md]
//
// Model access uses OPENAI_API_KEY and ANTHROPIC_API_KEY. Trials are
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
	armsFlag := flag.String("arms", "structured,plain", "arms to run")
	trials := flag.Int("trials", 5, "trials per task, model and arm")
	iterations := flag.Int("iterations", 20, "iteration budget per trial")
	budget := flag.Duration("budget", 30*time.Minute, "wall-clock budget per trial")
	out := flag.String("out", "", "report path (default bench/agent/REPORT.md)")
	dryRun := flag.Bool("dry-run", false, "validate tasks (hidden tests against the reference solutions) without calling any model")
	kernFlag := flag.String("kern", "", "a prebuilt kern binary to use instead of building one into bench/.build (lets several dry runs share one build)")
	reportFrom := flag.String("report-from", "", "score the trial records of an existing run directory into the report without calling any model (budgets are taken from the flags)")
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
		records, n := loadRecords(*reportFrom, tasks)
		writeReport(root, *out, writeAgentReport(records, tasks, n, *iterations, *budget, relRun(root, *reportFrom)), len(records), *reportFrom)
		return
	}
	runDir := filepath.Join(root, "bench", "agent", "runs", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	var records []trialRecord
	for _, ms := range strings.Split(*modelsFlag, ",") {
		provider, model, _ := strings.Cut(ms, ":")
		client, err := newClient(provider, model)
		if err != nil {
			fatalf("%v", err)
		}
		for _, arm := range strings.Split(*armsFlag, ",") {
			for _, t := range tasks {
				for trial := 1; trial <= *trials; trial++ {
					logf("task %s model %s arm %s trial %d", t.Name, ms, arm, trial)
					rec := runTrial(root, kernBin, runDir, client, ms, arm, t, trial, *iterations, *budget)
					records = append(records, rec)
					b, _ := json.MarshalIndent(rec, "", " ")
					if err := os.WriteFile(filepath.Join(runDir, fmt.Sprintf("%s_%s_%s_%d.json", t.Name, strings.ReplaceAll(ms, ":", "-"), arm, trial)), b, 0o644); err != nil {
						fatalf("recording trial: %v", err)
					}
					logf("  -> success=%v iterations=%d wall=%.0fs tokens=%d/%d", rec.Success, rec.Iterations, rec.WallSeconds, rec.InputTokens, rec.OutputTok)
				}
			}
		}
	}
	writeReport(root, *out, writeAgentReport(records, tasks, *trials, *iterations, *budget, relRun(root, runDir)), len(records), runDir)
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
		id := fmt.Sprintf("%s|%s|%s|%d", rec.Task, rec.Model, rec.Arm, rec.Trial)
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
