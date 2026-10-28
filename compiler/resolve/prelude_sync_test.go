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
	for _, name := range append([]string{"core"}, StdModules...) {
		std, err := os.ReadFile("../../std/" + name + ".kern")
		if err != nil {
			t.Fatal(err)
		}
		embedded, err := preludeFS.ReadFile("prelude/" + name + ".kern")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(std, embedded) {
			t.Fatalf("std/%s.kern differs from compiler/resolve/prelude/%s.kern: copy the embedded file to std/", name, name)
		}
	}
}
