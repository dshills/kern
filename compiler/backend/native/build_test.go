package native

import (
	"strings"
	"testing"
)

func TestHostTriple(t *testing.T) {
	tr := HostTriple()
	if !strings.Contains(tr, "-") || (!strings.HasPrefix(tr, "aarch64") && !strings.HasPrefix(tr, "x86_64")) {
		t.Fatalf("unexpected host triple %q", tr)
	}
}
