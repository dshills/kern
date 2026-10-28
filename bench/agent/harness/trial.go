package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// systemPrompt gives every model the same language reference and examples;
// the arms differ only in how diagnostics come back and which queries exist.
func systemPrompt(root string, arm string) string {
	ref, _ := os.ReadFile(filepath.Join(root, "docs", "ownership.md"))
	grammar, _ := os.ReadFile(filepath.Join(root, "spec", "grammar.ebnf"))
	prelude, _ := os.ReadFile(filepath.Join(root, "std", "core.kern"))
	ex1, _ := os.ReadFile(filepath.Join(root, "tests", "run", "pool_handles.kern"))
	ex2, _ := os.ReadFile(filepath.Join(root, "tests", "run", "scoped_sum.kern"))
	var b strings.Builder
	b.WriteString("You are a programmer working in kern, a systems language with second-class access (view/edit/own parameters, no references), static moves and traps. Edit the task's files with the tools until the task is done. Work in small verified steps: after each change, check the program and run it. When the program checks cleanly and prints the expected output, reply with exactly DONE.\n\n")
	if arm == "structured" {
		b.WriteString("The `check` tool returns structured JSON diagnostics: each has a stable code, the violated rule number, the byte span and related spans. Use the rule number to look up the rule in the reference below. The tools `explain_ownership`, `show_copies` and `show_allocations` answer semantic questions about places, copies and allocation sites; use them instead of guessing.\n\n")
	} else {
		b.WriteString("The `check` tool returns the compiler's plain-text diagnostics.\n\n")
	}
	b.WriteString("# Language reference\n\n")
	b.Write(ref)
	b.WriteString("\n\n# Grammar\n\n```\n")
	b.Write(grammar)
	b.WriteString("\n```\n\n# Standard library (prelude)\n\n```kern\n")
	b.Write(prelude)
	b.WriteString("\n```\n\n# Examples\n\n```kern\n")
	b.Write(ex1)
	b.WriteString("\n```\n\n```kern\n")
	b.Write(ex2)
	b.WriteString("\n```\n")
	return b.String()
}

// tools lists the tool definitions of an arm.
func tools(arm string) []toolDef {
	ts := []toolDef{
		{Name: "read_file", Description: "Read one of the task's files.", Params: map[string]string{"file": "file name"}},
		{Name: "write_file", Description: "Replace the whole content of one of the task's files.", Params: map[string]string{"file": "file name", "content": "new content"}},
		{Name: "check", Description: "Run the kern checker on the task's files and return its diagnostics; a diagnostic may carry a machine-applicable fix (a structured edit) that write_file can apply.", Params: map[string]string{}},
		{Name: "run", Description: "Run the program and return its standard output, standard error and exit status.", Params: map[string]string{}},
	}
	if arm == "structured" {
		ts = append(ts,
			toolDef{Name: "explain_ownership", Description: "Explain the ownership state, accesses and drop points of every place intersecting a byte span of a file (JSON).", Params: map[string]string{"file": "file name", "start": "byte offset", "end": "byte offset"}},
			toolDef{Name: "show_copies", Description: "List every bitwise and explicit copy in the program with its location, type and size (JSON).", Params: map[string]string{}},
			toolDef{Name: "show_allocations", Description: "List every allocation site in the program (JSON).", Params: map[string]string{}},
		)
	}
	return ts
}

type toolDef struct {
	Name        string
	Description string
	Params      map[string]string
}

// runTrial drives one agent trial and scores it.
func runTrial(root, kernBin, runDir string, client llmClient, modelName, arm string, t *task, trial, iterations int, budget time.Duration) trialRecord {
	rec := trialRecord{Task: t.Name, Model: modelName, Arm: arm, Trial: trial}
	work, err := os.MkdirTemp(runDir, t.Name+"-"+arm+"-")
	if err != nil {
		rec.Error = err.Error()
		return rec
	}
	for _, f := range t.Files {
		b, err := os.ReadFile(filepath.Join(t.Dir, "start", f))
		if err == nil {
			err = os.WriteFile(filepath.Join(work, f), b, 0o644)
		}
		if err != nil {
			rec.Error = "workspace: " + err.Error()
			return rec
		}
	}
	startUnsafe := unsafeLineCount(work, t.Files)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	conv := newConversation(systemPrompt(root, arm), tools(arm))
	conv.user(fmt.Sprintf("%s\n\nFiles: %s. Expected output of the program is given in the task; make the program check cleanly and print it.", t.Prompt, strings.Join(t.Files, ", ")))
	start := time.Now()
	prevPassed := false
	for it := 1; it <= iterations; it++ {
		if ctx.Err() != nil {
			rec.TimedOut = true
			break
		}
		rec.Iterations = it
		reply, calls, usage, err := client.step(ctx, conv)
		rec.InputTokens += usage.input
		rec.OutputTok += usage.output
		if err != nil {
			rec.Error = err.Error()
			if ctx.Err() != nil {
				rec.TimedOut = true
			}
			break
		}
		if len(calls) == 0 {
			if strings.Contains(strings.ToUpper(reply), "DONE") {
				break
			}
			conv.user("Continue: use the tools to check and run the program, or reply DONE when it is finished.")
			continue
		}
		for _, c := range calls {
			res := execTool(ctx, root, kernBin, work, t, arm, c)
			rec.Calls = append(rec.Calls, toolCallLog{Iteration: it, Tool: c.name, Args: truncate(c.args, 4000), Result: truncate(res, 4000)})
			conv.toolResult(c, res)
		}
		// hidden tests after every iteration: a passing-to-failing
		// transition is one regression
		ok, _ := hiddenPasses(ctx, root, kernBin, t, work)
		if prevPassed && !ok {
			rec.Regressions++
		}
		prevPassed = ok
	}
	rec.WallSeconds = time.Since(start).Seconds()
	if ctx.Err() != nil {
		rec.TimedOut = true
	}
	ok, _ := hiddenPasses(context.Background(), root, kernBin, t, work)
	rec.Success = ok && !rec.TimedOut
	rec.UnsafeAdded = max(0, unsafeLineCount(work, t.Files)-startUnsafe)
	if !rec.Success {
		// a failed or timed-out trial scores the budget maximum
		rec.Iterations = iterations
		rec.WallSeconds = budget.Seconds()
	}
	return rec
}

// execTool runs one tool call in the trial's working directory.
func execTool(ctx context.Context, root, kernBin, work string, t *task, arm string, c toolCall) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(c.args), &args)
	str := func(k string) string {
		v, _ := args[k].(string)
		return v
	}
	num := func(k string) int {
		switch v := args[k].(type) {
		case float64:
			return int(v)
		case string:
			var n int
			_, _ = fmt.Sscanf(v, "%d", &n)
			return n
		}
		return 0
	}
	allowed := func(name string) bool {
		base := filepath.Base(name)
		for _, f := range t.Files {
			if f == base {
				return true
			}
		}
		return false
	}
	files := func() []string {
		var out []string
		for _, f := range t.Files {
			out = append(out, filepath.Join(work, f))
		}
		return out
	}
	switch c.name {
	case "read_file":
		if !allowed(str("file")) {
			return "error: not a task file"
		}
		b, err := os.ReadFile(filepath.Join(work, filepath.Base(str("file"))))
		if err != nil {
			return "error: " + err.Error()
		}
		return string(b)
	case "write_file":
		if !allowed(str("file")) {
			return "error: not a task file"
		}
		if err := os.WriteFile(filepath.Join(work, filepath.Base(str("file"))), []byte(str("content")), 0o644); err != nil {
			return "error: " + err.Error()
		}
		return "ok"
	case "check":
		if arm == "structured" {
			out, _ := runCmdCtx(ctx, work, kernBin, append([]string{"check"}, files()...)...)
			return out
		}
		out, err := runCmdCtx(ctx, work, kernBin, append([]string{"check", "--human"}, files()...)...)
		if err != nil && strings.TrimSpace(out) == "" {
			return "error: the checker could not run: " + err.Error()
		}
		if strings.TrimSpace(out) == "" {
			return "ok: no diagnostics"
		}
		return out
	case "run":
		out, err := runProgram(ctx, work, kernBin, files())
		if err != nil {
			return out + "\n(exit: " + err.Error() + ")"
		}
		return out
	case "explain_ownership":
		if !allowed(str("file")) {
			return "error: not a task file"
		}
		req := map[string]any{"schema_version": "0.2", "command": "explain-ownership", "files": files(), "span": map[string]any{"file": filepath.Join(work, filepath.Base(str("file"))), "start": num("start"), "end": num("end")}}
		rb, _ := json.Marshal(req)
		out, _ := runCmdStdin(ctx, work, string(rb), kernBin, "explain-ownership")
		return out
	case "show_copies":
		out, _ := runCmdCtx(ctx, work, kernBin, append([]string{"show-copies"}, files()...)...)
		return out
	case "show_allocations":
		out, _ := runCmdCtx(ctx, work, kernBin, append([]string{"show-allocations"}, files()...)...)
		return out
	}
	return "error: unknown tool"
}

// runProgram runs the task's program in the interpreter with a 60 s limit
// within the trial's deadline.
func runProgram(parent context.Context, work, kernBin string, files []string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, kernBin, append([]string{"run"}, files...)...)
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// hiddenPasses runs the hidden suite against the sources in dir.
func hiddenPasses(ctx context.Context, root, kernBin string, t *task, dir string) (bool, string) {
	var files []string
	for _, f := range t.Files {
		files = append(files, filepath.Join(dir, f))
	}
	if t.Hidden.MustCheckClean {
		out, err := runCmdCtx(ctx, dir, kernBin, append([]string{"check"}, files...)...)
		if err != nil || !strings.Contains(out, `"ok":true`) {
			return false, "check reports diagnostics"
		}
	}
	out, err := runProgram(ctx, dir, kernBin, files)
	if err != nil || out != t.Hidden.ExpectedOutput {
		return false, fmt.Sprintf("output %q (err %v), want %q", truncate(out, 200), err, truncate(t.Hidden.ExpectedOutput, 200))
	}
	if t.Hidden.MaxAllocations != nil {
		if n := countField(ctx, dir, kernBin, "show-allocations", "sites", files); n > *t.Hidden.MaxAllocations {
			return false, fmt.Sprintf("%d allocation sites, at most %d allowed", n, *t.Hidden.MaxAllocations)
		}
	}
	if t.Hidden.MaxCopies != nil {
		if n := countField(ctx, dir, kernBin, "show-copies", "copies", files); n > *t.Hidden.MaxCopies {
			return false, fmt.Sprintf("%d copies, at most %d allowed", n, *t.Hidden.MaxCopies)
		}
	}
	for _, ep := range t.Hidden.ExtraPrograms {
		hidden := filepath.Join(t.Dir, "hidden", ep.File)
		tmp := filepath.Join(dir, "hidden_"+filepath.Base(ep.File))
		b, err := os.ReadFile(hidden)
		if err != nil {
			return false, "missing hidden program " + ep.File
		}
		_ = os.WriteFile(tmp, b, 0o644)
		// the hidden program replaces the task's main file
		var alt []string
		for _, f := range files {
			if filepath.Base(f) != t.Hidden.Main {
				alt = append(alt, f)
			}
		}
		alt = append(alt, tmp)
		out, err := runProgram(ctx, dir, kernBin, alt)
		_ = os.Remove(tmp)
		if err != nil || out != ep.ExpectedOutput {
			return false, fmt.Sprintf("hidden program %s: output %q, want %q", ep.File, truncate(out, 200), truncate(ep.ExpectedOutput, 200))
		}
	}
	return true, "ok"
}

// validateReference runs the hidden suite against the task's reference solution.
func validateReference(ctx context.Context, root, kernBin string, t *task) (bool, string) {
	return hiddenPasses(ctx, root, kernBin, t, filepath.Join(t.Dir, "reference"))
}

// countField counts the entries of a machine-API list result; any failure
// counts as unbounded (the hidden test then fails).
func countField(ctx context.Context, dir, kernBin, cmd, field string, files []string) int {
	out, err := runCmdCtx(ctx, dir, kernBin, append([]string{cmd}, files...)...)
	if err != nil {
		return 1 << 30
	}
	var resp struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(out), &resp) != nil {
		return 1 << 30
	}
	var items []json.RawMessage
	if err := json.Unmarshal(resp.Result[field], &items); err != nil {
		return 1 << 30
	}
	return len(items)
}

var unsafeRE = regexp.MustCompile(`\bunsafe\b`)

func unsafeLineCount(dir string, files []string) int {
	n := 0
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		for _, line := range strings.Split(string(b), "\n") {
			if unsafeRE.MatchString(line) {
				n++
			}
		}
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func runCmd(dir, name string, args ...string) (string, error) {
	return runCmdCtx(context.Background(), dir, name, args...)
}

// runCmdCtx runs a command under the trial's deadline.
func runCmdCtx(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runCmdStdin(ctx context.Context, dir, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func findRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repository root (go.mod) not found above %s", dir)
		}
		dir = parent
	}
}

// median of a float slice (0 when empty).
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64{}, xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}
