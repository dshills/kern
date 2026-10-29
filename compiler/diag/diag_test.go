package diag

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistryShape(t *testing.T) {
	for code, info := range Registry {
		if info.Rule == "" || info.Summary == "" || info.Phase < 0 || info.Phase > 5 {
			t.Errorf("%s: incomplete registry entry %+v", code, info)
		}
		if !Valid(code) {
			t.Errorf("%s: Valid false", code)
		}
	}
	if Valid("NOT_A_CODE") {
		t.Fatal("unregistered code reported valid")
	}
}

func TestNewPanicsOnUnregistered(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	New("NOT_A_CODE", "f", Span{}, "x")
}

func TestWriteAndSort(t *testing.T) {
	ds := []Diagnostic{
		New("ACC_CONFLICT", "b.kern", Span{Start: 5, End: 6}, "b"),
		New("OWN_USE_AFTER_MOVE", "a.kern", Span{Start: 9, End: 10}, "a2"),
		New("ACC_CONFLICT", "a.kern", Span{Start: 9, End: 10}, "a1"),
	}
	Sort(ds)
	if ds[0].File != "a.kern" || ds[0].Code != "ACC_CONFLICT" || ds[1].Code != "OWN_USE_AFTER_MOVE" || ds[2].File != "b.kern" {
		t.Fatalf("sort order: %v", ds)
	}
	var buf bytes.Buffer
	if err := Write(&buf, Response{SchemaVersion: SchemaVersion, OK: false, Diagnostics: ds}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "\"message\":\"a1\"") || !strings.Contains(out, "\"schema_version\":\"0.3\"") {
		t.Fatalf("wire format: %q", out)
	}
	var back map[string]any
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	d := back["diagnostics"].([]any)[0].(map[string]any)
	for _, k := range []string{"code", "severity", "rule", "file", "span", "related", "fix", "op_index"} {
		if _, ok := d[k]; !ok {
			t.Errorf("diagnostic lacks %s", k)
		}
	}
	if !strings.Contains(Render(ds[0]), "ACC_CONFLICT") {
		t.Fatal("render")
	}
}
