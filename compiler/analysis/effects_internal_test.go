package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/runtime/hosted"
)

// runtimeTrapSites are the runtimes' trap sites for intrinsics: the hosted
// runtime's function that raises the trap (empty when only the interpreter
// does, as for its raw-memory checks), the interpreter's intrinsic key, and
// the kind. A new trap in either runtime is added here and to trapping.
var runtimeTrapSites = []struct {
	intrinsic, native, kind string
}{
	{"Shared.new", "kern_shared_new", "oom"},
	{"Mutex.new", "kern_mutex_new", "oom"},
	{"Condvar.new", "kern_condvar_new", "oom"},
	{"thread.scope", "kern_scope_new", "oom"},
	{"Shared.clone", "kern_shared_clone", "overflow"},
	{"Mutex.lock", "kern_mutex_lock", "explicit"},
	{"Condvar.wait", "kern_condvar_wait", "explicit"},
	{"SystemAllocator.alloc", "kern_sysalloc_alloc", "explicit"},
	{"SystemAllocator.realloc", "kern_sysalloc_realloc", "explicit"},
	{"SystemAllocator.alloc_zeroed", "kern_sysalloc_alloc_zeroed", "explicit"},
	{"SystemAllocator.free", "", "explicit"},
	{"simd_shuffle", "", "bounds"},
	{"asm", "", "explicit"},
	{"ptr_copy", "", "explicit"},
	{"testing.require_hooks", "", "explicit"},
	{"testing.fail_next_alloc", "", "explicit"},
	{"testing.fail_next_spawn", "", "explicit"},
	{"testing.seed_shared_count", "", "overflow"},
	{"time_sleep_ns", "kern_time_sleep_ns", "explicit"},
	{"time_wall_ns", "kern_time_wall_ns", "explicit"},
	{"str_parse_f64", "kern_str_parse_f64", "oom"},
}

// The analysis's trapping table lists every kind the runtimes raise for an
// intrinsic (specs/effect-trap-kinds R5); each site is checked to exist in
// the runtime sources by name, so a renamed or removed site fails here.
func TestTrappingMatchesRuntime(t *testing.T) {
	read := func(parts ...string) string {
		b, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	native := hosted.Source // every unit of the hosted runtime
	interp := read("compiler", "interp", "intrinsics.go")
	for _, s := range runtimeTrapSites {
		if s.native != "" && !strings.Contains(native, s.native+"(") {
			t.Errorf("%s: %s is not in kern_rt.c", s.intrinsic, s.native)
		}
		if !strings.Contains(interp, `"`+s.intrinsic+`":`) {
			t.Errorf("%s is not an interpreter intrinsic", s.intrinsic)
		}
		k, ok := trapping[s.intrinsic]
		if !ok || k&kindOf(s.kind) == 0 {
			t.Errorf("trapping[%s] = %v, missing %s", s.intrinsic, k.list(), s.kind)
		}
	}
	// every runtime object allocation that traps oom goes through
	// kern_alloc: its definition and the four call sites (Shared.new,
	// Mutex.new, Condvar.new, thread.scope). A spawn allocates with
	// kern_try_alloc and fails as Err(SpawnErr) (specs/runtime RC3).
	if strings.Count(native, "kern_alloc(") != 5 {
		t.Errorf("kern_alloc call sites changed: recheck the oom entries")
	}
}

// allKinds is exactly the listed kinds, and a kindSet can hold them.
func TestAllKindsCoversTrapKinds(t *testing.T) {
	if len(trapKinds) > 16 {
		t.Fatalf("%d kinds do not fit a kindSet", len(trapKinds))
	}
	if got := allKinds.list(); strings.Join(got, " ") != strings.Join(trapKinds, " ") {
		t.Errorf("allKinds lists %v", got)
	}
}
