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
	modelsFlag := flag.String("models", "openai:gpt-6.1-sol,anthropic:claude-opus-5-5", "provider:model pairs")
	armsFlag := flag.String("arms", "structured,plain", "arms to run")
	trials := flag.Int("trials", 5, "trials per task, model and arm")
	iterations := flag.Int("iterations", 20, "iteration budget per trial")
	budget := flag.Duration("budget", 30*time.Minute, "wall-clock budget per trial")
	out := flag.String("out", "", "report path (default bench/agent/REPORT.md)")
	dryRun := flag.Bool("dry-run", false, "validate tasks (hidden tests against the reference solutions) without calling any model")
	flag.Parse()
	root, err := findRoot()
	if err != nil {
		fatalf("%v", err)
	}
	kernBin := filepath.Join(root, "bench", ".build", "kern")
	if msg, err := runCmd(root, "go", "build", "-o", kernBin, "./cmd/kern"); err != nil {
		fatalf("building kern: %v: %s", err, msg)
	}
	tasks := loadTasks(root)
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
			bad, detail2 := hiddenPasses(context.Background(), root, kernBin, t, filepath.Join(t.Dir, "start"))
			fmt.Printf("%s: starting sources %s hidden tests %s\n", t.Name, map[bool]string{true: "already pass (task is trivial!)", false: "fail as intended"}[bad], detail2)
			if !ok || bad {
				failed = true
			}
		}
		if failed {
			os.Exit(1)
		}
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
	report := writeAgentReport(records, tasks, *trials, *iterations, *budget)
	path := *out
	if path == "" {
		path = filepath.Join(root, "bench", "agent", "REPORT.md")
	}
	if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("wrote %s (%d trials under %s)\n", path, len(records), runDir)
}

// loadTasks reads bench/agent/tasks/<name>/{task.md,hidden.json,start/,reference/}.
func loadTasks(root string) []*task {
	dir := filepath.Join(root, "bench", "agent", "tasks")
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
