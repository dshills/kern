package native

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/source"
)

func TestHostTriple(t *testing.T) {
	tr := HostTriple()
	if !strings.Contains(tr, "-") || (!strings.HasPrefix(tr, "aarch64") && !strings.HasPrefix(tr, "x86_64")) {
		t.Fatalf("unexpected host triple %q", tr)
	}
}

// The runtime object is compiled once per configuration into the user's
// cache directory and reused while its fingerprint holds;
// KERN_NO_RUNTIME_CACHE compiles the source with the program instead.
// Every build runs.
func TestRuntimeObjectCache(t *testing.T) {
	if !Available() {
		t.Skip("no clang")
	}
	t.Setenv("KERN_NO_RUNTIME_CACHE", "") // the cache on, whatever the caller set
	for _, v := range []string{"MACOSX_DEPLOYMENT_TARGET", "C_INCLUDE_PATH", "CPATH"} {
		t.Setenv(v, "") // the baseline the changes below depart from
	}
	home := t.TempDir()
	t.Setenv("HOME", home)                                    // macOS: ~/Library/Caches
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache")) // elsewhere
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory")
	}
	cache := filepath.Join(base, "kern", "runtime")
	dir := t.TempDir()
	path := filepath.Join(dir, "p.kern")
	if err := os.WriteFile(path, []byte("module p\nfunc main() -> i32 { print_u64(42u64); println(); return 0i32 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	objects := func() int {
		es, _ := filepath.Glob(filepath.Join(cache, "*.o"))
		return len(es)
	}
	build := func(name, level string) {
		t.Helper()
		out := filepath.Join(dir, name)
		if ds, err := Build([]*source.File{f}, out, Options{Opt: level}); err != nil || len(ds) > 0 {
			t.Fatalf("build %s: %v %v", name, ds, err)
		}
		got, err := exec.Command(out).Output()
		if err != nil || string(got) != "42\n" {
			t.Fatalf("%s printed %q (%v), want 42", name, got, err)
		}
	}
	build("a", "release")
	if n := objects(); n != 1 {
		t.Fatalf("%d runtime objects after the first build, want 1", n)
	}
	build("b", "release")
	if n := objects(); n != 1 {
		t.Fatalf("%d runtime objects after a second release build, want 1 (reused)", n)
	}
	ms, _ := filepath.Glob(filepath.Join(cache, "*.deps"))
	if len(ms) != 1 {
		t.Fatalf("manifests %v, want one", ms)
	}
	manifest := ms[0]
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// object, fingerprint, then the compiler and configuration files
	lines := strings.SplitN(string(data), "\n", 3)
	if len(lines) != 3 || !strings.Contains(lines[2], "clang") {
		t.Fatalf("manifest %q lists no compiler", data)
	}
	if _, _, ok := cachedObject(cache, manifest); !ok {
		t.Fatalf("a fresh manifest reads as changed")
	}
	// a listed file that changed (a compiler upgraded in place) and
	// another fingerprint each compile afresh, rewriting the manifest
	// (the same code, so the same object)
	first := strings.SplitN(lines[2], "\n", 2)[0]
	for name, stale := range map[string]string{
		"file":        lines[0] + "\n" + lines[1] + "\n1 1 " + strings.SplitN(first, " ", 3)[2] + "\n",
		"fingerprint": lines[0] + "\n0000\n" + lines[2],
	} {
		if err := os.WriteFile(manifest, []byte(stale), 0o644); err != nil {
			t.Fatal(err)
		}
		build("b-"+name, "release")
		if again, _ := os.ReadFile(manifest); string(again) != string(data) || objects() != 1 {
			t.Fatalf("after a %s change: manifest %q, %d objects; want the manifest rewritten and 1 object", name, again, objects())
		}
	}
	// a header created where it shadows one the runtime includes changes
	// the fingerprint
	inc := t.TempDir()
	t.Setenv("C_INCLUDE_PATH", inc)
	build("c", "release")
	withPath, _ := os.ReadFile(manifest)
	if err := os.WriteFile(filepath.Join(inc, "stddef.h"), []byte("/* shadow */\n#include_next <stddef.h>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build("d", "release")
	if shadowed, _ := os.ReadFile(manifest); string(shadowed) == string(withPath) {
		t.Fatalf("a shadowing header left the fingerprint as it was")
	}
	t.Setenv("C_INCLUDE_PATH", "")
	// another configuration is another key; another deployment target
	// another object
	build("e", "debug")
	if n := objects(); n != 2 {
		t.Fatalf("%d runtime objects after a debug build, want 2", n)
	}
	before := objects()
	t.Setenv("MACOSX_DEPLOYMENT_TARGET", "13.0")
	build("f", "debug")
	if runtime.GOOS == "darwin" && objects() != before+1 {
		t.Fatalf("%d runtime objects after changing MACOSX_DEPLOYMENT_TARGET, want %d", objects(), before+1)
	}
	t.Setenv("MACOSX_DEPLOYMENT_TARGET", "")
	n := objects()
	t.Setenv("KERN_NO_RUNTIME_CACHE", "1")
	build("g", "debug")
	build("h", "release")
	if objects() != n {
		t.Fatalf("%d runtime objects with the cache off, want %d", objects(), n)
	}
	if es, _ := filepath.Glob(filepath.Join(cache, "build-*")); len(es) > 0 {
		t.Fatalf("leftover build directories %v", es)
	}
}

// filesUnchanged compares each listed file's size and modification time;
// an edited file, a missing one, a malformed line or a missing manifest
// is a change.
func TestFilesUnchanged(t *testing.T) {
	dir := t.TempDir()
	hdr := filepath.Join(dir, "a b.cfg")
	if err := os.WriteFile(hdr, []byte("-O2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(hdr)
	if err != nil {
		t.Fatal(err)
	}
	lines := fmt.Sprintf("%d %d %s\n", fi.Size(), fi.ModTime().UnixNano(), hdr)
	if !filesUnchanged(lines) {
		t.Fatal("unchanged file reads as changed")
	}
	if filesUnchanged("x 1 " + hdr + "\n") {
		t.Fatal("malformed line reads as unchanged")
	}
	if err := os.WriteFile(hdr, []byte("-O3\n-g\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if filesUnchanged(lines) {
		t.Fatal("edited file reads as unchanged")
	}
	if err := os.Remove(hdr); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := cachedObject(dir, filepath.Join(dir, "none.deps")); filesUnchanged(lines) || ok {
		t.Fatal("missing file or manifest reads as unchanged")
	}
}

// driverFiles finds the configuration files and the -cc1 executable in a
// driver's account of its job, in both the -v and the -### forms, and
// nothing without a -cc1 job.
func TestDriverFiles(t *testing.T) {
	head := `Homebrew clang version 23.1.4
Target: arm64-apple-darwin27.2.0
InstalledDir: /opt/homebrew/Cellar/llvm/23.1.4/bin
Configuration file: /opt/homebrew/Cellar/llvm/23.1.4/etc/clang/arm64-apple-darwin27.cfg
 (in-process)
`
	want := []string{"/opt/homebrew/Cellar/llvm/23.1.4/etc/clang/arm64-apple-darwin27.cfg", "/opt/homebrew/Cellar/llvm/23.1.4/bin/clang-23"}
	for _, cc1 := range []string{
		` "/opt/homebrew/Cellar/llvm/23.1.4/bin/clang-23" "-cc1" "-triple" "arm64-apple-macosx27.2.0" "-O2"`,
		` "/opt/homebrew/Cellar/llvm/23.1.4/bin/clang-23" -cc1 -triple arm64-apple-macosx27.2.0 -O2`,
	} {
		if got := driverFiles(head + cc1 + "\n"); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("driverFiles = %q, want %q", got, want)
		}
	}
	if got := driverFiles("clang version 1\nConfiguration file: /x.cfg\n"); got != nil {
		t.Fatalf("driverFiles without a -cc1 job = %q, want none", got)
	}
}
