// Package source loads kern source files and defines byte-offset spans.
package source

import (
	"os"
	"strings"
	"unicode/utf8"
)

// File is one loaded source file. Offsets are byte offsets into Bytes.
type File struct {
	Name  string
	Bytes []byte
}

// Load reads a file from disk.
func Load(name string) (*File, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return &File{Name: name, Bytes: b}, nil
}

// Headers returns the values of leading "//!" lines (fixture headers), each
// without the marker and trimmed. Blank lines between headers are permitted.
func (f *File) Headers() []string {
	var out []string
	for _, line := range strings.Split(string(f.Bytes), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "//!") {
			break
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(t, "//!")))
	}
	return out
}

// Line returns the 1-based line number of a byte offset.
func (f *File) Line(off int) int {
	if off > len(f.Bytes) {
		off = len(f.Bytes)
	}
	return 1 + strings.Count(string(f.Bytes[:off]), "\n")
}

// OnBoundary reports whether off is a UTF-8 character boundary within the file.
func (f *File) OnBoundary(off int) bool {
	if off < 0 || off > len(f.Bytes) {
		return false
	}
	if off == len(f.Bytes) {
		return true
	}
	return utf8.RuneStart(f.Bytes[off])
}
