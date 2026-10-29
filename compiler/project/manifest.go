// Package project finds a program's files: the project root and its
// manifest kern.mod, the entry files, and the files of every module the
// program imports (specs/module-discovery/SPEC.md). The command line calls
// it once per invocation before any other source loading; it imports
// nothing from cmd/.
package project

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dshills/kern/compiler/diag"
)

// ManifestName is the manifest's file name, which marks the project root.
const ManifestName = "kern.mod"

// Manifest is a parsed kern.mod: every field is optional, nil when the
// manifest did not set it. Entry is the entry file or files, relative to
// the project root.
type Manifest struct {
	Path          string // the manifest's name as the root search found it
	SchemaVersion *string
	Entry         []string
	Opt           *string
	Env           *string
	Target        *string
	Output        *string
}

// ParseManifest reads a manifest's bytes (spec §5): an empty or
// whitespace-only file is the empty manifest; otherwise one JSON object
// whose known fields are validated, the last occurrence of a repeated
// field counting. Every failure is PROJECT_INVALID naming the field or
// quoting the parser.
func ParseManifest(path string, b []byte) (*Manifest, []diag.Diagnostic) {
	m := &Manifest{Path: path}
	if len(bytes.TrimSpace(b)) == 0 {
		return m, nil
	}
	invalid := func(format string, args ...any) []diag.Diagnostic {
		return []diag.Diagnostic{diag.New("PROJECT_INVALID", path, diag.Span{}, format, args...)}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return nil, invalid("%s must hold a JSON object", ManifestName)
		}
		return nil, invalid("%s: %v", ManifestName, err)
	}
	if raw == nil {
		return nil, invalid("%s must hold a JSON object", ManifestName)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return nil, invalid("%s: %v", ManifestName, err)
		}
		return nil, invalid("%s: trailing content after the manifest object", ManifestName)
	}
	str := func(field string) (*string, []diag.Diagnostic) {
		v, ok := raw[field]
		if !ok {
			return nil, nil
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, invalid("%s: field %q must be a string", ManifestName, field)
		}
		return &s, nil
	}
	for field := range raw {
		switch field {
		case "schema_version", "entry", "opt", "env", "target", "output":
		default:
			return nil, invalid("%s: unknown field %q", ManifestName, field)
		}
	}
	var ds []diag.Diagnostic
	if m.SchemaVersion, ds = str("schema_version"); ds != nil {
		return nil, ds
	}
	if m.SchemaVersion != nil && *m.SchemaVersion != diag.SchemaVersion {
		return nil, invalid("%s: field %q must be %q", ManifestName, "schema_version", diag.SchemaVersion)
	}
	if v, ok := raw["entry"]; ok {
		var one string
		var many []string
		switch {
		case json.Unmarshal(v, &one) == nil && !bytes.Equal(bytes.TrimSpace(v), []byte("null")):
			many = []string{one}
		case json.Unmarshal(v, &many) == nil && many != nil:
		default:
			return nil, invalid("%s: field %q must be a string or an array of strings", ManifestName, "entry")
		}
		if len(many) == 0 {
			return nil, invalid("%s: field %q must name at least one file", ManifestName, "entry")
		}
		for _, e := range many {
			if !relativePath(e) {
				return nil, invalid("%s: field %q must hold relative paths without \"..\" components, not %q", ManifestName, "entry", e)
			}
		}
		m.Entry = many
	}
	if m.Opt, ds = str("opt"); ds != nil {
		return nil, ds
	}
	if m.Opt != nil && *m.Opt != "debug" && *m.Opt != "release" {
		return nil, invalid("%s: field %q must be \"debug\" or \"release\"", ManifestName, "opt")
	}
	if m.Env, ds = str("env"); ds != nil {
		return nil, ds
	}
	if m.Env != nil && *m.Env != "hosted" && *m.Env != "freestanding" {
		return nil, invalid("%s: field %q must be \"hosted\" or \"freestanding\"", ManifestName, "env")
	}
	if m.Target, ds = str("target"); ds != nil {
		return nil, ds
	}
	if m.Output, ds = str("output"); ds != nil {
		return nil, ds
	}
	if m.Output != nil && (*m.Output == "" || filepath.IsAbs(*m.Output)) {
		return nil, invalid("%s: field %q must be a relative path", ManifestName, "output")
	}
	return m, nil
}

// relativePath reports whether p is a non-empty relative path with no
// ".." component.
func relativePath(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	for _, c := range strings.Split(filepath.ToSlash(p), "/") {
		if c == ".." {
			return false
		}
	}
	return true
}

// loadManifest reads and parses the manifest at path (a display name,
// accessed under cwd).
func loadManifest(cwd, path string) (*Manifest, []diag.Diagnostic) {
	b, err := os.ReadFile(onDisk(cwd, path))
	if err != nil {
		return nil, []diag.Diagnostic{diag.New("PROJECT_INVALID", path, diag.Span{}, "%s: %v", ManifestName, err)}
	}
	return ParseManifest(path, b)
}

func (m *Manifest) String() string {
	if m == nil {
		return "<no manifest>"
	}
	return fmt.Sprintf("manifest %s entry=%v", m.Path, m.Entry)
}
