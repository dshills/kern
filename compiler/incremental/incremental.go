// Package incremental implements cached, per-module checking (PLAN.md 4.1):
// a module's cache entry is keyed by its source, the interface files of
// its imports, the target, the build configuration, the compiler version
// and the schema version, so any change invalidates the entry and, through
// the interface hashes, its dependents. Incremental and full `check`
// produce identical diagnostics.
package incremental

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/iface"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// CompilerVersion is part of every cache key.
const CompilerVersion = "kern-0.4"

// Config is the build configuration the key covers (PLAN.md §0).
type Config struct {
	Target string
	Opt    string
	Env    string
}

// Stats reports what a Check did.
type Stats struct {
	Modules   int               // modules in the request
	Hits      int               // modules served from the cache
	Rechecked []string          // modules (re)checked in this run, sorted
	Ifaces    map[string]string // interface hash per module after this run
}

type entry struct {
	Key   string            `json:"key"`
	Iface string            `json:"iface"` // hash of the module's interface file
	Diags []diag.Diagnostic `json:"diags"`
}

type module struct {
	name    string
	files   []*source.File
	imports []string
	srcHash string
	main    bool
}

// Check checks srcs through the cache at dir (created on demand) and
// returns the same diagnostics a full check returns. Modules are taken in
// dependency order: a module whose key (its source, its imports'
// interface hashes, the configuration and the embedded library) has an
// entry is served from it, and any other is checked alone against the
// resolved program and stored, so an edit re-checks the edited module and
// the dependents whose imports' interfaces changed, not the whole program.
// When every module hits, nothing is resolved.
func Check(dir string, srcs []*source.File, cfg Config) ([]diag.Diagnostic, Stats, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, Stats{}, err
	}
	mods, parseDiags := group(srcs)
	if len(parseDiags) > 0 {
		// parse errors are reported by the full path; they are not cached
		return full(srcs), Stats{Modules: len(mods), Rechecked: names(mods)}, nil
	}
	st := Stats{Modules: len(mods)}
	order, ok := depOrder(mods)
	if !ok {
		// a cycle or an import outside the request: the full check reports
		// it, and nothing is cached
		st.Rechecked = names(mods)
		return full(srcs), st, nil
	}
	// the keys from the stored interface hashes: when every module hits,
	// the stored diagnostics are the answer
	stored := map[string]string{}
	var out []diag.Diagnostic
	allHit := true
	for _, m := range order {
		k, ok := m.keyWith(cfg, stored)
		e, err := load(dir, k)
		if !ok || err != nil {
			allHit = false
			break
		}
		stored[m.name] = e.Iface
		out = append(out, e.Diags...)
	}
	if allHit {
		st.Hits = len(mods)
		st.Ifaces = stored
		diag.Sort(out)
		if out == nil {
			out = []diag.Diagnostic{}
		}
		return out, st, nil
	}
	prog := resolve.Resolve(srcs)
	if ds := prog.Diags; len(ds) > 0 {
		// a request with resolution errors is not cached: the check phase
		// did not run, so no per-module result would be complete
		diag.Sort(ds)
		st.Rechecked = names(mods)
		return ds, st, nil
	}
	srcBytes := map[string][]byte{}
	for _, m := range mods {
		for _, f := range m.files {
			srcBytes[f.Name] = f.Bytes
		}
	}
	// the interface hash also records whether the module had diagnostics:
	// a full check does not report dependents of a module that failed to
	// resolve, so dependents must be re-keyed when that state changes
	ifaceHash := map[string]string{}
	out = nil
	for _, m := range order {
		k, _ := m.keyWith(cfg, ifaceHash)
		if e, err := load(dir, k); err == nil {
			st.Hits++
			ifaceHash[m.name] = e.Iface
			out = append(out, e.Diags...)
			continue
		}
		name := m.name
		mds := check.CheckModules(prog, func(n string) bool { return n == name })
		if mds == nil {
			mds = []diag.Diagnostic{}
		}
		rm := prog.Modules[m.name]
		if rm == nil {
			return nil, st, fmt.Errorf("incremental: module %s did not resolve", m.name)
		}
		h := sha256.New()
		h.Write(iface.Interface(rm, srcBytes))
		_, _ = fmt.Fprintf(h, "\nerrors=%v\n", len(mds) > 0)
		ifaceHash[m.name] = hex.EncodeToString(h.Sum(nil))
		if err := store(dir, &entry{Key: k, Iface: ifaceHash[m.name], Diags: mds}); err != nil {
			return nil, st, err
		}
		st.Rechecked = append(st.Rechecked, m.name)
		out = append(out, mds...)
	}
	sort.Strings(st.Rechecked)
	st.Ifaces = ifaceHash
	diag.Sort(out)
	if out == nil {
		out = []diag.Diagnostic{}
	}
	return out, st, nil
}

// libraryHash covers the embedded prelude and std modules, so that a
// change to them re-keys every entry even when CompilerVersion is not
// bumped.
var libraryHash = func() string {
	h := sha256.New()
	for _, name := range append([]string{"core"}, resolve.StdModules...) {
		src, _ := resolve.StdSource(name)
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", name, len(src))
		h.Write(src)
	}
	return hex.EncodeToString(h.Sum(nil))
}()

// keyWith is the module's cache key given its imports' interface hashes;
// false when one of them is not known. Imports of the embedded library are
// covered by libraryHash.
func (m *module) keyWith(cfg Config, ifaces map[string]string) (string, bool) {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%s|%s|%v|", CompilerVersion, diag.SchemaVersion, iface.Version, libraryHash, cfg.Target, cfg.Opt, cfg.Env, m.srcHash, m.main)
	for _, imp := range m.imports {
		if isLibrary(imp) {
			continue
		}
		ih, ok := ifaces[imp]
		if !ok {
			return "", false
		}
		_, _ = fmt.Fprintf(h, "%s=%s|", imp, ih)
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

func isLibrary(imp string) bool { return strings.HasPrefix(imp, "std/") }

// depOrder lists the modules with every module after the request modules
// it imports; false on a cycle or an import that is neither a request
// module nor a library module.
func depOrder(mods []*module) ([]*module, bool) {
	byName := map[string]*module{}
	for _, m := range mods {
		byName[m.name] = m
	}
	var order []*module
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(m *module) bool
	visit = func(m *module) bool {
		switch state[m.name] {
		case 1:
			return false
		case 2:
			return true
		}
		state[m.name] = 1
		for _, imp := range m.imports {
			if isLibrary(imp) {
				continue
			}
			im := byName[imp]
			if im == nil || !visit(im) {
				return false
			}
		}
		state[m.name] = 2
		order = append(order, m)
		return true
	}
	for _, m := range mods {
		if !visit(m) {
			return nil, false
		}
	}
	return order, true
}

// full is the uncached path.
func full(srcs []*source.File) []diag.Diagnostic {
	prog := resolve.Resolve(srcs)
	if len(prog.Diags) > 0 {
		diag.Sort(prog.Diags)
		return prog.Diags
	}
	ds := check.Check(prog)
	diag.Sort(ds)
	if ds == nil {
		ds = []diag.Diagnostic{}
	}
	return ds
}

// group parses the request files into modules (by module name) with their
// import lists and source hashes.
func group(srcs []*source.File) ([]*module, []diag.Diagnostic) {
	byName := map[string]*module{}
	var order []string
	var pds []diag.Diagnostic
	for _, f := range srcs {
		af, ds := parser.Parse(f)
		if len(ds) > 0 {
			pds = append(pds, ds...)
			continue
		}
		m := byName[af.Module]
		if m == nil {
			m = &module{name: af.Module}
			byName[af.Module] = m
			order = append(order, af.Module)
		}
		m.files = append(m.files, f)
		for _, imp := range af.Imports {
			m.imports = append(m.imports, imp.Path)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name == "main" {
				m.main = true
			}
		}
	}
	var out []*module
	for _, n := range order {
		m := byName[n]
		sort.Strings(m.imports)
		m.imports = uniq(m.imports)
		sort.Slice(m.files, func(i, j int) bool { return m.files[i].Name < m.files[j].Name })
		h := sha256.New()
		for _, f := range m.files {
			_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f.Name, len(f.Bytes))
			h.Write(f.Bytes)
		}
		m.srcHash = hex.EncodeToString(h.Sum(nil))
		out = append(out, m)
	}
	return out, pds
}

func names(mods []*module) []string {
	var out []string
	for _, m := range mods {
		out = append(out, m.name)
	}
	sort.Strings(out)
	return out
}

func uniq(xs []string) []string {
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func load(dir, key string) (*entry, error) {
	b, err := os.ReadFile(filepath.Join(dir, key+".json"))
	if err != nil {
		return nil, err
	}
	var e entry
	if err := json.Unmarshal(b, &e); err != nil || e.Key != key {
		return nil, fmt.Errorf("corrupt cache entry %s", key)
	}
	return &e, nil
}

func store(dir string, e *entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, e.Key+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, e.Key+".json")); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
