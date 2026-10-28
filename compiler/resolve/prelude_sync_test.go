package resolve

import (
	"bytes"
	"os"
	"testing"
)

// TestStdCoreMatchesPrelude keeps std/core.kern, the copy that people, the
// benchmark harness (unsafe-line count) and the agent benchmark's system
// prompt read, identical to the embedded prelude the compiler resolves.
// Edit prelude/core.kern, then copy it to std/core.kern.
func TestStdCoreMatchesPrelude(t *testing.T) {
	std, err := os.ReadFile("../../std/core.kern")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := preludeFS.ReadFile("prelude/core.kern")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(std, embedded) {
		t.Fatal("std/core.kern differs from compiler/resolve/prelude/core.kern: copy the prelude to std/core.kern")
	}
}
