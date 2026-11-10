package analysis

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/runtime.md's entry table is generated from the two runtime headers
// (specs/runtime RK1): each prototype with the one-line contract written
// above it. TestRuntimeEntryTable fails when the document's table is not
// what the headers give; -update-runtime-doc rewrites it.

var updateRuntimeDoc = flag.Bool("update-runtime-doc", false, "rewrite the entry table in docs/runtime.md from the runtime headers")

const (
	entriesBegin = "<!-- entries:begin (generated from the runtime headers: go test ./compiler/analysis -run TestRuntimeEntryTable -update-runtime-doc) -->"
	entriesEnd   = "<!-- entries:end -->"
)

var (
	sectionLine = regexp.MustCompile(`^// ---- (.*?)(?: ----)?$`)
	protoLine   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_ \*]*[ \*]([A-Za-z_][A-Za-z0-9_]*)\(.*\);$`)
)

// entryTable is the markdown table of one header's entries, a section per
// unit. Every prototype must carry a contract: the line of comment
// directly above it.
func entryTable(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	lines := strings.Split(string(b), "\n")
	hooks, rows := false, 0
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "#ifdef KERN_TEST_HOOKS"):
			hooks = true
		case strings.HasPrefix(line, "#endif"):
			hooks = false
		}
		if m := sectionLine.FindStringSubmatch(line); m != nil {
			title := strings.TrimSuffix(strings.TrimSpace(m[1]), " ----")
			// a title that runs over two lines of comment
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "// ") && strings.HasSuffix(lines[i+1], "----") {
				title += " " + strings.TrimSuffix(strings.TrimPrefix(lines[i+1], "// "), " ----")
			}
			fmt.Fprintf(&out, "\n**%s**\n\n| Entry | Contract |\n|---|---|\n", strings.TrimSpace(title))
			continue
		}
		m := protoLine.FindStringSubmatch(line)
		if m == nil || strings.HasPrefix(line, "typedef") || strings.HasPrefix(line, "extern") || strings.HasPrefix(line, "static") {
			continue
		}
		if i == 0 || !strings.HasPrefix(lines[i-1], "// ") {
			t.Errorf("%s: %s has no contract line above its prototype", filepath.Base(path), m[1])
			continue
		}
		contract := strings.TrimPrefix(lines[i-1], "// ")
		if hooks {
			contract += " (A `--test-hooks` build only.)"
		}
		// a pipe would end the cell
		proto := strings.ReplaceAll(strings.TrimSuffix(line, ";"), "|", `\|`)
		fmt.Fprintf(&out, "| `%s` | %s |\n", proto, strings.ReplaceAll(contract, "|", `\|`))
		rows++
	}
	if rows < 20 {
		t.Fatalf("%s: only %d entries read", path, rows)
	}
	return out.String()
}

func TestRuntimeEntryTable(t *testing.T) {
	root := filepath.Join("..", "..")
	var want strings.Builder
	want.WriteString(entriesBegin + "\n\n")
	want.WriteString("### The hosted runtime (`runtime/hosted/src/kern_rt.h`)\n")
	want.WriteString(entryTable(t, filepath.Join(root, "runtime", "hosted", "src", "kern_rt.h")))
	want.WriteString("\n### The freestanding runtime (`runtime/freestanding/src/kern_rt_fs.h`)\n")
	want.WriteString(entryTable(t, filepath.Join(root, "runtime", "freestanding", "src", "kern_rt_fs.h")))
	want.WriteString("\n" + entriesEnd)
	doc := filepath.Join(root, "docs", "runtime.md")
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	i, j := strings.Index(text, entriesBegin), strings.Index(text, entriesEnd)
	if i < 0 || j < i {
		t.Fatalf("docs/runtime.md has no entry table markers")
	}
	if got := text[i : j+len(entriesEnd)]; got != want.String() {
		if *updateRuntimeDoc {
			if err := os.WriteFile(doc, []byte(text[:i]+want.String()+text[j+len(entriesEnd):]), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatalf("docs/runtime.md's entry table is stale; run go test ./compiler/analysis -run TestRuntimeEntryTable -update-runtime-doc")
	}
}
