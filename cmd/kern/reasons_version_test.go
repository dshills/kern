package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

const reasonsFixture = "testdata/reasons_version.kern"

// reasonsGolden04 is the fixture's 0.4 answer, recorded from the compiler
// before specs/unproven-reasons (28f2751).
const reasonsGolden04 = "testdata/reasons_version.04.golden"

// counting returns a classifier that wraps ClassifyBounds and the number
// of calls made through it, for one test.
func counting() (analysis.Classifier, *int) {
	n := new(int)
	return func(prog *resolve.Program, srcs []*source.File, snap *analysis.BoundsSnapshot, want func(diag.BoundsCheck) bool) (map[int]analysis.Reason, analysis.ClassifyStats) {
		*n++
		return analysis.ClassifyBounds(prog, srcs, snap, want)
	}, n
}

// show-bounds-checks carries reason and hint at 0.5 only, on the unproven
// bounds entry only; a 0.4 answer is byte-identical to the previous
// compiler's and never classifies (specs/unproven-reasons R3, R5).
func TestBoundsReasonsByVersion(t *testing.T) {
	golden, err := os.ReadFile(reasonsGolden04)
	if err != nil {
		t.Fatal(err)
	}
	req04 := `{"schema_version":"0.4","command":"show-bounds-checks","files":["` + reasonsFixture + `"]}`
	if code, out, _ := invoke(t, req04, "show-bounds-checks"); code != 0 || out != string(golden) {
		t.Errorf("0.4: exit %d\n%s\nwant\n%s", code, out, golden)
	}
	if base := os.Getenv("KERN_BASE"); base != "" {
		cmd := exec.Command(base, "show-bounds-checks")
		cmd.Stdin = strings.NewReader(req04)
		out, err := cmd.Output()
		if err != nil || !bytes.Equal(out, golden) {
			t.Errorf("KERN_BASE answers differently from the golden file (%v):\n%s", err, out)
		}
	}
	for _, args := range [][]string{{"show-bounds-checks", reasonsFixture}, {"show-bounds-checks"}} {
		stdin := ""
		if len(args) == 1 {
			stdin = `{"schema_version":"0.5","command":"show-bounds-checks","files":["` + reasonsFixture + `"]}`
		}
		code, out, _ := invoke(t, stdin, args...)
		var resp struct {
			SchemaVersion string                      `json:"schema_version"`
			Result        diag.ShowBoundsChecksResult `json:"result"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil || code != 0 || resp.SchemaVersion != "0.5" {
			t.Fatalf("%v: exit %d, %v\n%s", args, code, err, out)
		}
		if len(resp.Result.Checks) != 3 {
			t.Fatalf("%v: %d entries, want 3", args, len(resp.Result.Checks))
		}
		for _, c := range resp.Result.Checks {
			unproven := c.Kind == diag.CheckBounds && c.Label == diag.LabelRuntimeChecked
			if (c.Reason != nil) != unproven || (c.Hint != nil) != unproven {
				t.Errorf("%v: %s %s entry: reason %v, hint %v", args, c.Kind, c.Label, c.Reason, c.Hint)
			}
			// the guard after the access is a candidate that covers it (§3)
			if unproven && (*c.Reason != analysis.ReasonGuardNotOnEveryPath || !strings.HasPrefix(*c.Hint, "the guard at 7:8 does not establish `mid < len(xs)`")) {
				t.Errorf("%v: unproven entry %s: %s", args, *c.Reason, *c.Hint)
			}
		}
		if !strings.Contains(out, `"reason":null,"hint":null`) {
			t.Errorf("%v: a null reason and hint are not written as null:\n%s", args, out)
		}
	}
}

// Only a 0.5 request classifies (R5).
func TestBoundsReasonsClassifyOnlyAt05(t *testing.T) {
	src, err := source.Load(reasonsFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		wire  string
		calls int
	}{{"0.2", 0}, {"0.3", 0}, {"0.4", 0}, {"0.5", 1}} {
		classify, calls := counting()
		var out, errb bytes.Buffer
		req := Request{SchemaVersion: tc.wire, Command: "show-bounds-checks", Files: []string{reasonsFixture}}
		if code := showBoundsChecksWith(classify, req, tc.wire, []*source.File{src}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d: %s%s", tc.wire, code, out.String(), errb.String())
		}
		if *calls != tc.calls {
			t.Errorf("%s: %d classifications, want %d", tc.wire, *calls, tc.calls)
		}
	}
}

// A classifier that panics is an internal error, not a crash.
func TestBoundsReasonsClassifierPanic(t *testing.T) {
	src, err := source.Load(reasonsFixture)
	if err != nil {
		t.Fatal(err)
	}
	boom := func(*resolve.Program, []*source.File, *analysis.BoundsSnapshot, func(diag.BoundsCheck) bool) (map[int]analysis.Reason, analysis.ClassifyStats) {
		panic("boom")
	}
	var out, errb bytes.Buffer
	req := Request{SchemaVersion: "0.5", Command: "show-bounds-checks", Files: []string{reasonsFixture}}
	code := showBoundsChecksWith(boom, req, "0.5", []*source.File{src}, &out, &errb)
	if code == 0 || !strings.Contains(out.String()+errb.String(), "boom") {
		t.Errorf("exit %d: %s%s", code, out.String(), errb.String())
	}
}
