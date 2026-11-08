package tests

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sampleChangelog = `# Changelog

## [Unreleased]

## [v0.1.0] - 2026-11-08

First release.

- one
- two

## [v0.1.0-rc.1] - 2026-11-01

Candidate.

## [v0.0.9]

No date.

## [v0.0.8] - 2026-01-01

## [v0.0.7] - 2026-01-01

[v0.1.0]: https://github.com/dshills/kern/releases/tag/v0.1.0
`

// releaseCheck runs scripts/release-check.sh with the given arguments over
// a changelog file; it returns the exit code and standard error.
func releaseCheck(t *testing.T, changelog string, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	cl := filepath.Join(dir, "CHANGELOG.md")
	if err := os.WriteFile(cl, []byte(changelog), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{filepath.Join("..", "scripts", "release-check.sh")}, args...)...)
	cmd.Env = append(os.Environ(), "CHANGELOG="+cl)
	cmd.Dir, _ = os.Getwd()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, stderr.String()
}

// TestReleaseCheck covers scripts/release-check.sh (specs/release-v0.1 R4).
func TestReleaseCheck(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	notesPath := func(t *testing.T) string { return filepath.Join(t.TempDir(), "notes.md") }

	t.Run("valid", func(t *testing.T) {
		for tag, want := range map[string]string{
			"v0.1.0":      "First release.\n\n- one\n- two\n",
			"v0.1.0-rc.1": "Candidate.\n",
		} {
			n := notesPath(t)
			code, stderr := releaseCheck(t, sampleChangelog, tag, n)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", tag, code, stderr)
			}
			got, err := os.ReadFile(n)
			if err != nil || string(got) != want {
				t.Fatalf("%s: notes %q (%v), want %q", tag, got, err, want)
			}
		}
	})

	fails := []struct {
		name      string
		changelog string
		args      []string
		reason    string
	}{
		{"no v", sampleChangelog, []string{"0.1.0", "N"}, "semantic version"},
		{"two components", sampleChangelog, []string{"v0.1", "N"}, "semantic version"},
		{"leading zero", sampleChangelog, []string{"v01.0.0", "N"}, "semantic version"},
		{"empty prerelease", sampleChangelog, []string{"v0.1.0-", "N"}, "semantic version"},
		{"numeric prerelease leading zero", sampleChangelog, []string{"v0.1.0-01", "N"}, "semantic version"},
		{"build metadata", sampleChangelog, []string{"v0.1.0+b1", "N"}, "semantic version"},
		{"missing section", sampleChangelog, []string{"v0.2.0", "N"}, "no heading"},
		{"heading without date", sampleChangelog, []string{"v0.0.9", "N"}, "no heading"},
		{"empty section", sampleChangelog, []string{"v0.0.8", "N"}, "is empty"},
		{"dot is literal", strings.Replace(sampleChangelog, "## [v0.1.0] -", "## [v0x1x0] -", 1), []string{"v0.1.0", "N"}, "no heading"},
		{"duplicate heading", sampleChangelog + "\n## [v0.1.0] - 2026-11-09\n\nagain\n", []string{"v0.1.0", "N"}, "more than one"},
		{"no arguments", sampleChangelog, nil, "usage"},
		{"one argument", sampleChangelog, []string{"v0.1.0"}, "usage"},
		{"three arguments", sampleChangelog, []string{"v0.1.0", "N", "x"}, "usage"},
	}
	for _, f := range fails {
		t.Run(f.name, func(t *testing.T) {
			args := append([]string(nil), f.args...)
			n := notesPath(t)
			for i, a := range args {
				if a == "N" {
					args[i] = n
				}
			}
			code, stderr := releaseCheck(t, f.changelog, args...)
			if code != 1 || !strings.HasPrefix(stderr, "release-check: ") || !strings.Contains(stderr, f.reason) || strings.Count(stderr, "\n") != 1 {
				t.Fatalf("exit %d stderr %q, want exit 1 and one release-check line containing %q", code, stderr, f.reason)
			}
			if _, err := os.Stat(n); err == nil {
				t.Fatalf("a failed check wrote the notes file")
			}
		})
	}

	// the repository's own changelog has a section for every release heading
	if _, err := os.Stat(filepath.Join("..", "CHANGELOG.md")); err == nil {
		b, _ := os.ReadFile(filepath.Join("..", "CHANGELOG.md"))
		for line := range strings.SplitSeq(string(b), "\n") {
			if !strings.HasPrefix(line, "## [v") {
				continue
			}
			tag := strings.TrimPrefix(line, "## [")
			tag = tag[:strings.Index(tag, "]")]
			n := notesPath(t)
			code, stderr := releaseCheck(t, string(b), tag, n)
			if code != 0 {
				t.Fatalf("CHANGELOG.md %s: exit %d: %s", tag, code, stderr)
			}
		}
	}
}
