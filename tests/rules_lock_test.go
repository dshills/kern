package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
)

// TestRulesLock: the diagnostic registry's rule numbers are frozen in
// spec/rules.lock (Phase 5 spec stabilization): a registered code must keep
// its rule number, and a new code must be added to the lock deliberately.
func TestRulesLock(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "spec", "rules.lock"))
	if err != nil {
		t.Fatal(err)
	}
	locked := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, rule, _ := strings.Cut(line, " ")
		locked[code] = rule
	}
	for code, info := range diag.Registry {
		rule, ok := locked[string(code)]
		if !ok {
			t.Errorf("code %s is not in spec/rules.lock (add it with rule %s)", code, info.Rule)
			continue
		}
		if rule != info.Rule {
			t.Errorf("code %s has rule %s but the lock freezes %s", code, info.Rule, rule)
		}
	}
	for code := range locked {
		if _, ok := diag.Registry[diag.Code(code)]; !ok {
			t.Errorf("spec/rules.lock lists %s, which is no longer registered", code)
		}
	}
}
