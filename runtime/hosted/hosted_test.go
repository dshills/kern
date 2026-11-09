package hosted

import (
	"strings"
	"testing"
)

// Source is the whole runtime as one file: no include of a local file is
// left, every unit's and the header's text is in it, and the #line
// directives name the units (specs/runtime RA1). The runtime-object cache
// is keyed on this text, so an edit to any unit or to the header changes
// the key.
func TestSourceAmalgamates(t *testing.T) {
	fs := Files()
	if len(fs) < 10 {
		t.Fatalf("only %d runtime files embedded", len(fs))
	}
	for _, line := range strings.Split(Source, "\n") {
		if localInclude.MatchString(line) {
			t.Fatalf("an include of a local file is left in Source: %s", line)
		}
	}
	for name, body := range fs {
		if name == Main {
			continue
		}
		if !strings.Contains(fs[Main], `#include "`+name+`"`) {
			t.Errorf("%s is not included by %s", name, Main)
		}
		// every non-include line of the file is in Source
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) != "" && !strings.Contains(Source, line) {
				t.Fatalf("a line of %s is missing from Source: %q", name, line)
			}
		}
		if !strings.Contains(Source, `#line 1 "`+name+`"`) {
			t.Errorf("Source has no #line directive for %s", name)
		}
	}
}
