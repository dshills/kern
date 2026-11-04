package analysis_test

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/source"
)

var undefinedSym = regexp.MustCompile(`undefined symbol: ([A-Za-z_][A-Za-z0-9_]*)`)

// The hosted-only entries the analysis finds are exactly the symbols
// ld.lld reports undefined when the image is linked without the
// diagnostic, on both freestanding targets at --opt=debug
// (specs/freestanding-availability R4). native.Build is not guarded: the
// diagnostic is cmd/kern's, before it calls the build. Each program
// prints or passes on its hosted call's result, so no optimization
// deletes the call.
func TestHostedOnlyMatchesLinker(t *testing.T) {
	for _, tool := range []string{"clang", "ld.lld"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	progs := []string{`module m
import "std/time"
func main() -> i32 {
    t := time.wall_nanos()
    print_u64(t)
    return 0i32
}
`, `module m
import "std/io"
func main() -> i32 {
    print_u64(u64(io.io_argc()))
    return 0i32
}
`, `module m
import "std/time"
func nap() { print_u64(time.wall_nanos()) }
func main() -> i32 {
    nap()
    return 0i32
}
`}
	for i, src := range progs {
		for _, triple := range []string{"aarch64-unknown-none-elf", "x86_64-unknown-none-elf"} {
			mod := buildBudgeted(t, map[string]string{"m.kern": src}, "debug").mod
			res, err := analysis.HostedOnly(mod, triple)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "image")
			_, berr := native.Build([]*source.File{{Name: "m.kern", Bytes: []byte(src)}}, out, native.Options{Opt: "debug", Triple: triple, Freestanding: true})
			if berr == nil {
				t.Errorf("program %d %s: the image linked; want undefined symbols %v", i, triple, res.Entries)
				continue
			}
			set := map[string]bool{}
			for _, m := range undefinedSym.FindAllStringSubmatch(berr.Error(), -1) {
				set[m[1]] = true
			}
			var linker []string
			for s := range set {
				linker = append(linker, s)
			}
			sort.Strings(linker)
			if !reflect.DeepEqual(linker, res.Entries) || len(linker) == 0 {
				t.Errorf("program %d %s: ld.lld undefined %v, analysis %v\n%v", i, triple, linker, res.Entries, berr)
			}
		}
	}
}
