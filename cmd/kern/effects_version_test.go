package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// show-effects carries trap_kinds at 0.4 only, and the corrected traps of
// the runtime object operations at every version
// (specs/effect-trap-kinds R1, R3).
func TestEffectsByVersion(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "m.kern")
	src := "module m\nfunc lock() -> u64 {\n    m := Mutex.new(1u64)\n    return 0u64\n}\nfunc add(a: u64, b: u64) -> u64 { return a + b }\nfunc main() -> i32 {\n    x := lock() + add(1u64, 2u64)\n    return 0i32\n}\n"
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0.2", "0.3", "0.4"} {
		req := `{"schema_version":"` + v + `","command":"show-effects","files":["` + f + `"]}`
		code, out, _ := invoke(t, req, "show-effects")
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", v, code, out)
		}
		var resp struct {
			SchemaVersion string `json:"schema_version"`
			Result        struct {
				Functions []map[string]any `json:"functions"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.SchemaVersion != v {
			t.Errorf("%s: answered at %s", v, resp.SchemaVersion)
		}
		byName := map[string]map[string]any{}
		for _, fn := range resp.Result.Functions {
			name, ok := fn["name"].(string)
			if !ok {
				t.Errorf("%s: a function entry without a string name: %v", v, fn)
				continue
			}
			byName[name] = fn
			if _, has := fn["trap_kinds"]; has != (v == "0.4") {
				t.Errorf("%s: %v has trap_kinds %v", v, fn["name"], has)
			}
		}
		lock := byName["m.lock"]
		if lock == nil || lock["traps"] != true {
			t.Errorf("%s: a function creating a Mutex does not trap: %v", v, lock)
		}
		if v == "0.4" {
			if ks, _ := json.Marshal(lock["trap_kinds"]); string(ks) != `["oom"]` {
				t.Errorf("lock trap_kinds %s, want [\"oom\"]", ks)
			}
			if ks, _ := json.Marshal(byName["m.add"]["trap_kinds"]); string(ks) != `["overflow"]` {
				t.Errorf("add trap_kinds %s, want [\"overflow\"]", ks)
			}
		}
	}
	// files on the command line answer at the newest version
	_, out, _ := invoke(t, "", "show-effects", f)
	var resp struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.SchemaVersion != "0.4" {
		t.Errorf("command line: %v %s", err, out)
	}
}
