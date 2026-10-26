package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var citeRE = regexp.MustCompile(`\[([a-z0-9][a-z0-9.-]*)\]`)
var keyRE = regexp.MustCompile(`\*\*\[([a-z0-9][a-z0-9.-]*)\]\*\*`)

// referenceKeys reads the bracketed keys of docs/references.md.
func referenceKeys(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "docs", "references.md"))
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, m := range keyRE.FindAllStringSubmatch(string(b), -1) {
		keys[m[1]] = true
	}
	return keys
}

// TestCitations: every bracketed citation in the research report, the ADRs
// and the workload notes names an entry of docs/references.md, and every
// paragraph of the research report that names another language cites one
// (PLAN.md 4.3 lint).
func TestCitations(t *testing.T) {
	keys := referenceKeys(t)
	var files []string
	for _, pat := range []string{"research-report.md", "decisions/*.md", "workloads/*.md"} {
		m, _ := filepath.Glob(filepath.Join("..", "docs", pat))
		files = append(files, m...)
	}
	if len(files) < 3 {
		t.Fatal("documentation files not found")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range citeRE.FindAllStringSubmatch(string(b), -1) {
			if isCitationLike(m[1]) && !keys[m[1]] {
				t.Errorf("%s: citation [%s] is not an entry of docs/references.md", filepath.Base(f), m[1])
			}
		}
	}
	report, err := os.ReadFile(filepath.Join("..", "docs", "research-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	langRE := regexp.MustCompile(`\b(Hylo|Swift|Mojo|Rust|Zig|Austral|Hare|Vale|Cyclone|Pony|Ada|SPARK|C\+\+|Go)\b`)
	for i, para := range strings.Split(string(report), "\n\n") {
		// headings, tables, code blocks and list items are not prose claims
		p := strings.TrimSpace(para)
		if p == "" || strings.HasPrefix(p, "#") || strings.HasPrefix(p, "|") || strings.HasPrefix(p, "```") || strings.HasPrefix(p, "- ") || strings.HasPrefix(p, "1.") {
			continue
		}
		if langRE.MatchString(para) && !citeRE.MatchString(para) {
			t.Errorf("research-report.md paragraph %d names another language without a citation: %q", i, truncateStr(para, 80))
		}
	}
}

// isCitationLike filters bracketed text that is not a citation key (link
// text and code spans use other characters; keys are lower-case slugs with
// at least one letter and are not plain numbers).
func isCitationLike(s string) bool {
	if !strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") {
		return false
	}
	// keys are hyphenated slugs (vendor-topic-year); plain words such as
	// [unproven] or [T] are markers and [self.tail] is code, not a citation
	return strings.Contains(s, "-")
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
