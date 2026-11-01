package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/source"
)

// TestProcessFreestanding: std/process is hosted only, like std/io
// (specs/std-process §1 constraint 5). A freestanding build that calls it
// fails in the same way as one calling std/io: the image links against no
// runtime entry of either module, so the link reports the missing kern_*
// symbols. No new diagnostic is added for it.
func TestProcessFreestanding(t *testing.T) {
	if !native.Available() || !native.Linkable() {
		t.Skip("clang or ld.lld not available")
	}
	progs := map[string]string{
		"kern_io_": `module t
import "std/io"
func main() -> u64 {
    with r := io.read_file("x") {
        match r {
            Ok(v) => return 1u64
            Err(e) => return 2u64
        }
    }
    return 0u64
}
`,
		"kern_proc_": `module t
import "std/process"
func main() -> u64 {
    with r := process.env("PATH") {
        match r {
            Some(v) => return 1u64
            None => return 2u64
        }
    }
    return 0u64
}
`,
	}
	for prefix, src := range progs {
		files := []*source.File{{Name: "t.kern", Bytes: []byte(src)}}
		img := filepath.Join(t.TempDir(), "image")
		ds, err := native.Build(files, img, native.Options{Freestanding: true, Triple: native.FreestandingTriple})
		if len(ds) > 0 {
			t.Fatalf("%s: the program should check cleanly, got %s", prefix, render(ds))
		}
		if err == nil {
			t.Fatalf("%s: a freestanding build calling a hosted module linked", prefix)
		}
		if !strings.Contains(err.Error(), "undefined symbol: "+prefix) {
			t.Fatalf("%s: want an undefined %s* symbol, got %v", prefix, prefix, err)
		}
	}
}
