package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func cellOf(n, successes int, iters ...float64) *cell {
	return &cell{n: n, successes: successes, iters: iters}
}

// TestStructuredBetter pins the per-model win condition (PLAN.md 4.2,
// revised 2026-10-28): at least 5 trials in each arm and the same number
// in both, then more successes, then a strictly lower median iteration
// count; a tie is not better.
func TestStructuredBetter(t *testing.T) {
	five := []float64{3, 4, 5, 6, 7}
	cases := []struct {
		name string
		s, p *cell
		want bool
	}{
		{"missing structured", nil, cellOf(5, 5, five...), false},
		{"missing plain", cellOf(5, 5, five...), nil, false},
		{"too few trials", cellOf(4, 4, 1, 1, 1, 1), cellOf(4, 2, 9, 9, 9, 9), false},
		{"unequal trials", cellOf(6, 6, 1, 1, 1, 1, 1, 1), cellOf(5, 2, 9, 9, 9, 9, 9), false},
		{"more successes win", cellOf(5, 5, 9, 9, 9, 9, 9), cellOf(5, 4, 1, 1, 1, 1, 1), true},
		{"fewer successes lose", cellOf(5, 4, 1, 1, 1, 1, 1), cellOf(5, 5, 9, 9, 9, 9, 9), false},
		{"same successes, lower median wins", cellOf(5, 5, 2, 3, 4, 5, 6), cellOf(5, 5, 3, 4, 5, 6, 7), true},
		{"same successes, higher median loses", cellOf(5, 5, 3, 4, 5, 6, 7), cellOf(5, 5, 2, 3, 4, 5, 6), false},
		{"tie loses", cellOf(5, 5, five...), cellOf(5, 5, five...), false},
	}
	for _, c := range cases {
		if got := structuredBetter(c.s, c.p); got != c.want {
			t.Errorf("%s: structuredBetter = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMedian(t *testing.T) {
	cases := []struct {
		xs   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{4}, 4},
		{[]float64{5, 1, 3}, 3},
		{[]float64{4, 1, 3, 2}, 2.5},
	}
	for _, c := range cases {
		if got := median(c.xs); got != c.want {
			t.Errorf("median(%v) = %v, want %v", c.xs, got, c.want)
		}
	}
	xs := []float64{3, 1, 2}
	median(xs)
	if xs[0] != 3 || xs[1] != 1 || xs[2] != 2 {
		t.Errorf("median sorted its argument: %v", xs)
	}
}

// records makes n trials of one task, model and arm with the given
// successes and iteration counts (a failed trial scores the budget).
func records(task, model, arm string, success []bool, iters []int) []trialRecord {
	var out []trialRecord
	for i := range success {
		out = append(out, trialRecord{Task: task, Model: model, Arm: arm, Trial: i + 1, Success: success[i], Iterations: iters[i]})
	}
	return out
}

// TestWriteAgentReportWins counts wins over two models: a task is a win
// only when the structured arm is better for every model, and a task with
// fewer than 5 trials in a cell is a loss.
func TestWriteAgentReportWins(t *testing.T) {
	ok := []bool{true, true, true, true, true}
	oneFail := []bool{true, true, true, true, false}
	fast := []int{2, 3, 3, 4, 4}
	slow := []int{4, 5, 5, 6, 6}
	var recs []trialRecord
	// t1: better for both models (successes on A, median on B): a win
	recs = append(recs, records("t1", "A", "structured", ok, slow)...)
	recs = append(recs, records("t1", "A", "plain", oneFail, fast)...)
	recs = append(recs, records("t1", "B", "structured", ok, fast)...)
	recs = append(recs, records("t1", "B", "plain", ok, slow)...)
	// t2: better for A, a tie for B: a loss
	recs = append(recs, records("t2", "A", "structured", ok, fast)...)
	recs = append(recs, records("t2", "A", "plain", ok, slow)...)
	recs = append(recs, records("t2", "B", "structured", ok, fast)...)
	recs = append(recs, records("t2", "B", "plain", ok, fast)...)
	// t3: better for both but only 4 trials on one cell: a loss
	recs = append(recs, records("t3", "A", "structured", ok[:4], fast[:4])...)
	recs = append(recs, records("t3", "A", "plain", oneFail[:4], slow[:4])...)
	recs = append(recs, records("t3", "B", "structured", ok, fast)...)
	recs = append(recs, records("t3", "B", "plain", ok, slow)...)
	tasks := []*task{{Name: "t1", Kind: "debugging"}, {Name: "t2", Kind: "optimization"}, {Name: "t3", Kind: "review"}}
	rep := writeAgentReport(recs, tasks, 5, 20, 30*time.Minute, "bench/agent/runs/x")
	for _, want := range []string{
		"**1 win of 3 tasks**",
		"| t1 | | | **win** |",
		"| t2 | | | **loss** |",
		"| t3 | | | **loss** |",
		"| t1 | debugging | A | plain | 5 | 4 | 3.0 |",
		"Models: A, B. Trials per task, model and arm: 5.",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	if strings.Contains(rep, "threshold 8 of 10") {
		t.Errorf("the threshold line is for a ten-task suite only:\n%s", rep)
	}
}

func TestLoadRecords(t *testing.T) {
	dir := t.TempDir()
	for _, r := range []trialRecord{
		{Task: "t1", Model: "A", Arm: "plain", Trial: 1, Success: true, Iterations: 3},
		{Task: "t1", Model: "A", Arm: "plain", Trial: 3, Success: false, Iterations: 20},
		{Task: "t1", Model: "A", Arm: "structured", Trial: 2, Success: true, Iterations: 2},
	} {
		b, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(dir, r.Task+"_"+r.Model+"_"+r.Arm+"_"+string(rune('0'+r.Trial))+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recs, n := loadRecords(dir, []*task{{Name: "t1"}})
	if len(recs) != 3 || n != 3 {
		t.Fatalf("loadRecords: %d records, n=%d; want 3 records, n=3", len(recs), n)
	}
}

func TestLoadTasks(t *testing.T) {
	dir := t.TempDir()
	td := filepath.Join(dir, "t07_hex")
	if err := os.MkdirAll(filepath.Join(td, "start"), 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(td, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("task.md", "# Hex\n\nKind: refactoring\n\nDo the thing.\n")
	must("hidden.json", `{"main":"hex.kern","expected_output":"ok\n","must_check_clean":true,"max_runtime_bounds_checks":5,"required_functions":["hex.digit","hex.value"],"no_unsafe_added":true}`)
	must(filepath.Join("start", "hex.kern"), "module hex\n")
	must(filepath.Join("start", "aux.kern"), "module hex\n")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a task"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks := loadTasks(dir)
	if len(tasks) != 1 {
		t.Fatalf("loadTasks: %d tasks, want 1", len(tasks))
	}
	tk := tasks[0]
	if tk.Name != "t07_hex" || tk.Kind != "refactoring" {
		t.Errorf("task %q kind %q", tk.Name, tk.Kind)
	}
	if strings.Join(tk.Files, ",") != "aux.kern,hex.kern" {
		t.Errorf("files %v, want sorted aux.kern,hex.kern", tk.Files)
	}
	h := tk.Hidden
	if h.Main != "hex.kern" || h.ExpectedOutput != "ok\n" || !h.MustCheckClean || !h.NoUnsafeAdded {
		t.Errorf("hidden spec %+v", h)
	}
	if h.MaxRuntimeBoundsChecks == nil || *h.MaxRuntimeBoundsChecks != 5 || h.MaxCopies != nil {
		t.Errorf("bounds budget %v, copies %v", h.MaxRuntimeBoundsChecks, h.MaxCopies)
	}
	if strings.Join(h.RequiredFunctions, ",") != "hex.digit,hex.value" {
		t.Errorf("required functions %v", h.RequiredFunctions)
	}
}

func TestMergeUserTurns(t *testing.T) {
	block := func(text string) []map[string]any { return []map[string]any{{"type": "text", "text": text}} }
	msgs := []map[string]any{
		{"role": "user", "content": block("a")},
		{"role": "user", "content": block("b")},
		{"role": "assistant", "content": "x"},
		{"role": "user", "content": "plain string"},
		{"role": "user", "content": block("c")},
	}
	out := mergeUserTurns(msgs)
	if len(out) != 4 {
		t.Fatalf("merged to %d messages, want 4 (two leading user turns merge, a string content does not)", len(out))
	}
	if c, ok := out[0]["content"].([]map[string]any); !ok || len(c) != 2 || c[1]["text"] != "b" {
		t.Errorf("first message content %v", out[0]["content"])
	}
	if out[3]["content"].([]map[string]any)[0]["text"] != "c" {
		t.Errorf("last message content %v", out[3]["content"])
	}
}

func TestParamsSchema(t *testing.T) {
	s := paramsSchema(toolDef{Name: "x", Params: map[string]string{"file": "the file", "text": "its text"}})
	props := s["properties"].(map[string]any)
	if len(props) != 2 || props["file"].(map[string]any)["description"] != "the file" {
		t.Errorf("properties %v", props)
	}
	req := s["required"].([]string)
	if len(req) != 2 {
		t.Errorf("required %v", req)
	}
	if _, ok := paramsSchema(toolDef{Name: "none"})["required"]; ok {
		t.Errorf("a tool without parameters lists required ones")
	}
}

func TestUnsafeLineCount(t *testing.T) {
	dir := t.TempDir()
	src := "module m\nfunc f() {\n    unsafe {\n        x := 1u8\n    }\n    y := 2u8 // nothing here\n    unsafe { z := 3u8 }\n    w := unsafer(1u8)\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "a.kern"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := unsafeLineCount(dir, []string{"a.kern", "missing.kern"}); n != 2 {
		t.Errorf("unsafeLineCount = %d, want 2 (two lines with the word; a longer word and a missing file count nothing)", n)
	}
	// the count is of lines mentioning the word, so a comment counts too:
	// the metric is a line count, not a parse
	if err := os.WriteFile(filepath.Join(dir, "b.kern"), []byte("// unsafe here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := unsafeLineCount(dir, []string{"b.kern"}); n != 1 {
		t.Errorf("unsafeLineCount of a comment = %d, want 1", n)
	}
}

func TestTruncateAndRelRun(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("ab", 3); got != "ab" {
		t.Errorf("truncate short = %q", got)
	}
	root := t.TempDir()
	if got := relRun(root, filepath.Join(root, "bench", "agent", "runs", "x")); got != filepath.Join("bench", "agent", "runs", "x") {
		t.Errorf("relRun inside root = %q", got)
	}
	other := t.TempDir()
	if got := relRun(root, other); got != other {
		t.Errorf("relRun outside root = %q, want the path as given", got)
	}
}

// The wafer provider sends OpenAI Responses requests to Wafer's
// /v1/responses whatever endpoint WAFER_API_URL names.
func TestWaferClientURL(t *testing.T) {
	t.Setenv("WAFER_API_KEY", "k")
	for _, base := range []string{"https://pass.wafer.ai/v1/chat/completions", "https://pass.wafer.ai/v1", "https://pass.wafer.ai/", "https://pass.wafer.ai"} {
		t.Setenv("WAFER_API_URL", base)
		c, err := newClient("wafer", "GLM-5.3", "")
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if oc, ok := c.(*openaiClient); !ok || oc.url != "https://pass.wafer.ai/v1/responses" || oc.model != "GLM-5.3" {
			t.Errorf("%s: client %+v", base, c)
		}
	}
	// a host that merely contains v1 is kept whole
	t.Setenv("WAFER_API_URL", "https://v1.wafer.example/v1beta")
	c, err := newClient("wafer", "m", "max")
	if err != nil {
		t.Fatal(err)
	}
	if oc, ok := c.(*openaiClient); !ok || oc.url != "https://v1.wafer.example/v1beta/v1/responses" {
		t.Errorf("v1 inside a name: %+v", c)
	}
	t.Setenv("WAFER_API_URL", "")
	if _, err := newClient("wafer", "GLM-5.3", ""); err == nil || !strings.Contains(err.Error(), "WAFER_API_URL") {
		t.Errorf("a missing URL: %v", err)
	}
	t.Setenv("WAFER_API_KEY", "")
	if _, err := newClient("wafer", "GLM-5.3", ""); err == nil || !strings.Contains(err.Error(), "WAFER_API_KEY") {
		t.Errorf("a missing key: %v", err)
	}
}

// -effort reaches the Responses request as reasoning.effort, is left out
// when unset, and is refused for anthropic rather than silently ignored.
func TestEffortInRequest(t *testing.T) {
	var (
		mu  sync.Mutex
		got map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"DONE"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	for _, effort := range []string{"max", ""} {
		c := &openaiClient{key: "k", model: "m", effort: effort, url: srv.URL}
		conv := newConversation("sys", nil)
		conv.user("hi")
		if _, _, _, err := c.step(context.Background(), conv); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		r, has := got["reasoning"].(map[string]any)
		mu.Unlock()
		if effort == "" && has {
			t.Errorf("no effort set, but the request has reasoning %v", r)
		}
		if effort != "" && (!has || r["effort"] != effort) {
			t.Errorf("effort %q: the request has reasoning %v", effort, got["reasoning"])
		}
	}
	t.Setenv("ANTHROPIC_API_KEY", "k")
	if _, err := newClient("anthropic", "m", "max"); err == nil || !strings.Contains(err.Error(), "-effort") {
		t.Errorf("anthropic with -effort: %v", err)
	}
}

// TestQueriesArm (the three-arm design): the queries arm gets plain-text
// diagnostics and the query tools; the plain arm is neither told of the
// queries nor able to call them.
func TestQueriesArm(t *testing.T) {
	names := func(arm string) map[string]bool {
		out := map[string]bool{}
		for _, td := range tools(arm) {
			out[td.Name] = true
		}
		return out
	}
	if q, p, s := names("queries"), names("plain"), names("structured"); !q["show_bounds_checks"] || p["show_bounds_checks"] || len(q) != len(s) {
		t.Errorf("tools: queries %v, plain %v, structured %v", q, p, s)
	}
	root := t.TempDir()
	sq, sp, ss := systemPrompt(root, "queries"), systemPrompt(root, "plain"), systemPrompt(root, "structured")
	// the documented arm reads the repository's document itself
	if sd := systemPrompt(filepath.Join("..", "..", ".."), "documented"); !strings.Contains(sd, "# Which bounds checks the compiler proves") || strings.Contains(sd, "show_bounds_checks") || len(names("documented")) != len(names("plain")) {
		t.Errorf("the documented arm's prompt or tools: %.400s", sd)
	}
	if sr := systemPrompt(filepath.Join("..", "..", ".."), "rules"); !strings.Contains(sr, "## What proves a check") || strings.Contains(sr, "midpoint") || strings.Contains(sr, "To make such an access") {
		t.Errorf("the rules arm's document is not the rules alone: %.400s", sr)
	}
	if !strings.Contains(sq, "plain-text diagnostics") || !strings.Contains(sq, querySentence) || strings.Contains(sq, "structured JSON") {
		t.Errorf("the queries arm's prompt: %.400s", sq)
	}
	if strings.Contains(sp, "show_bounds_checks") || !strings.Contains(ss, querySentence) {
		t.Error("the plain prompt mentions the queries, or the structured one lacks them")
	}
	work := t.TempDir()
	tk := &task{Name: "x", Files: []string{"a.kern"}}
	if got := execTool(context.Background(), root, "/nonexistent/kern", work, tk, "plain", toolCall{name: "show_bounds_checks", args: "{}"}); got != "error: unknown tool" {
		t.Errorf("the plain arm called a query: %q", got)
	}
}

// TestDecompositionAndEnds: with a queries arm present the report scores
// queries against plain and structured against queries, and counts how
// failed trials ended per arm.
func TestDecompositionAndEnds(t *testing.T) {
	ok := []bool{true, true, true, true, true}
	none := []bool{false, false, false, false, false}
	fast := []int{2, 3, 3, 4, 4}
	slow := []int{4, 5, 5, 6, 6}
	var recs []trialRecord
	recs = append(recs, records("t1", "A", "structured", ok, fast)...)
	recs = append(recs, records("t1", "A", "queries", ok, slow)...)
	plain := records("t1", "A", "plain", none, slow)
	for i := range plain {
		plain[i].EndReason = "done"
	}
	plain[4].EndReason = ""
	recs = append(recs, plain...)
	rep := writeAgentReport(recs, []*task{{Name: "t1", Kind: "optimization"}}, 5, 20, 30*time.Minute, "r")
	for _, want := range []string{
		"| t1 | optimization | A | queries | 5 | 5 | 5.0 |",
		"| `queries` over `plain` | the semantic queries, diagnostics held to plain text | 1 of 1 | t1 5/0 |",
		"| `structured` over `queries` | JSON diagnostics, the queries held constant | 1 of 1 | t1 5/5 |",
		"| plain | 4 | 0 | 0 | 0 | 1 |",
		"| structured | 0 | 0 | 0 | 0 | 0 |",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
	two := writeAgentReport(records("t1", "A", "structured", ok, fast), []*task{{Name: "t1"}}, 5, 20, time.Minute, "r")
	if strings.Contains(rep, "`documented` over") {
		t.Errorf("a pair whose arm did not run is scored:\n%s", rep)
	}
	if strings.Contains(two, "Arm decomposition") || strings.Contains(two, "How failed") {
		t.Errorf("a run without the queries arm or failures has the extra sections:\n%s", two)
	}
}

// TestRedoErrors: a trial whose model call failed is run again under the
// same model, effort, arm and trial number; the failed record is kept as
// .json.error, the trial that succeeded at the API is left alone, and the
// report is written from the directory.
func TestRedoErrors(t *testing.T) {
	var (
		mu      sync.Mutex
		efforts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if re, ok := body["reasoning"].(map[string]any); ok {
			mu.Lock()
			efforts = append(efforts, fmt.Sprint(re["effort"]))
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"DONE"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	t.Setenv("WAFER_API_KEY", "k")
	t.Setenv("WAFER_API_URL", srv.URL+"/v1")
	tdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tdir, "start"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "start", "a.kern"), []byte("module a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tk := &task{Name: "t1", Dir: tdir, Files: []string{"a.kern"}}
	run := t.TempDir()
	write := func(rec trialRecord) {
		b, _ := json.Marshal(rec)
		if err := os.WriteFile(filepath.Join(run, recordName(rec.Task, rec.Model, rec.Arm, rec.Trial)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := "wafer:GLM-5.3@max"
	write(trialRecord{Task: "t1", Model: m, Arm: "structured", Trial: 1, EndReason: "error", Error: "timeout"})
	write(trialRecord{Task: "t1", Model: m, Arm: "plain", Trial: 1, EndReason: "done"})
	out := filepath.Join(t.TempDir(), "report.md")
	redo(t.TempDir(), "/nonexistent/kern", run, []*task{tk}, 2, 3, time.Minute, out)
	mu.Lock()
	defer mu.Unlock()
	if len(efforts) != 1 || efforts[0] != "max" {
		t.Errorf("model calls with efforts %v, want one at max", efforts)
	}
	name := filepath.Join(run, recordName("t1", m, "structured", 1))
	if _, err := os.Stat(name + ".error"); err != nil {
		t.Errorf("the failed record is not kept: %v", err)
	}
	var rec trialRecord
	b, err := os.ReadFile(name)
	if err == nil {
		err = json.Unmarshal(b, &rec)
	}
	if err != nil || rec.EndReason != "done" || rec.Model != m || rec.Trial != 1 {
		t.Errorf("the re-run record: %+v (%v)", rec, err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("no report: %v", err)
	}
}

// TestLoadRuns merges the records of two run directories (directories
// with different trials per cell are refused, by fatalf).
func TestLoadRuns(t *testing.T) {
	tk := []*task{{Name: "t1"}}
	mk := func(model string, trials int) string {
		d := t.TempDir()
		for i := 1; i <= trials; i++ {
			b, _ := json.Marshal(trialRecord{Task: "t1", Model: model, Arm: "plain", Trial: i})
			if err := os.WriteFile(filepath.Join(d, recordName("t1", model, "plain", i)), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	// a run directory list may carry spaces after the commas
	recs, n := loadRuns([]string{mk("a:x", 5), " " + mk("b:y", 5)}, tk)
	if len(recs) != 10 || n != 5 {
		t.Errorf("merged %d records, %d trials; want 10 and 5", len(recs), n)
	}
}

// TestPromptStatesBudget: a hidden bounds-check budget must appear in the
// prompt, as "no runtime-checked bounds checks" for zero.
func TestPromptStatesBudget(t *testing.T) {
	zero, four := 0, 4
	for _, c := range []struct {
		prompt string
		budget *int
		ok     bool
	}{
		{"report **no runtime-checked bounds checks** in", &zero, true},
		{"report **at most 4 runtime-checked bounds checks** in", &zero, false},
		{"report **at most 4 runtime-checked bounds checks** in", &four, true},
		{"anything", nil, true},
	} {
		tk := &task{Name: "t", Prompt: c.prompt, Hidden: hiddenSpec{MaxRuntimeBoundsChecks: c.budget}}
		if got := promptStatesBudget(tk); (got == "") != c.ok {
			t.Errorf("%q: %q", c.prompt, got)
		}
	}
}
