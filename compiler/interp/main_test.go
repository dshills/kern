package interp

import (
	"os"
	"testing"

	"github.com/dshills/kern/compiler/heapfloor"
)

// TestMain gives the tests the heap floor the kern command sets for a
// program it interprets.
func TestMain(m *testing.M) {
	heapfloor.Set(heapfloor.Default)
	os.Exit(m.Run())
}
