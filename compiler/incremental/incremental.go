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
	key     string
}

// Check checks srcs through the cache at dir (created on demand) and
// returns the same diagnostics a full check returns.
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
	// keys in dependency order; a module whose imports are not all keyed
	// (a cycle or a missing import) is a miss
	keyed := map[string]*entry{}
	byName := map[string]*module{}
	for _, m := range mods {
		byName[m.name] = m
	}
	var order []*module
	visiting := map[string]bool{}
	var visit func(m *module) bool
	visit = func(m *module) bool {
		if m.key != "" {
			return true
		}
		if visiting[m.name] {
			return false
		}
		visiting[m.name] = true
		defer delete(visiting, m.name)
		h := sha256.New()
		_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%s|%v|", CompilerVersion, diag.SchemaVersion, iface.Version, cfg.Target, cfg.Opt, cfg.Env, m.srcHash, m.main)
		for _, imp := range m.imports {
			im := byName[imp]
			if im == nil || !visit(im) {
				return false
			}
			e, err := load(dir, im.key)
			if err != nil {
				return false
			}
			keyed[imp] = e
			_, _ = fmt.Fprintf(h, "%s=%s|", imp, e.Iface)
		}
		m.key = hex.EncodeToString(h.Sum(nil))
		order = append(order, m)
		return true
	}
	allHit := true
	for _, m := range mods {
		if !visit(m) {
			allHit = false
			break
		}
		if e, err := load(dir, m.key); err == nil {
			keyed[m.name] = e
		} else {
			allHit = false
		}
	}
	if allHit {
		var out []diag.Diagnostic
		for _, m := range mods {
			out = append(out, keyed[m.name].Diags...)
		}
		st.Hits = len(mods)
		st.Ifaces = map[string]string{}
		for n, e := range keyed {
			st.Ifaces[n] = e.Iface
		}
		diag.Sort(out)
		if out == nil {
			out = []diag.Diagnostic{}
		}
		return out, st, nil
	}
	// a full run; every module's entry is (re)written so that dependents
	// keyed by the new interface hashes hit next time
	prog := resolve.Resolve(srcs)
	ds := prog.Diags
	if len(ds) > 0 {
		// a request with resolution errors is not cached: the check phase
		// did not run, so no per-module result would be complete
		diag.Sort(ds)
		st.Rechecked = names(mods)
		return ds, st, nil
	}
	ds = check.Check(prog)
	diag.Sort(ds)
	srcBytes := map[string][]byte{}
	fileMod := map[string]string{}
	for _, m := range mods {
		for _, f := range m.files {
			srcBytes[f.Name] = f.Bytes
			fileMod[f.Name] = m.name
		}
	}
	perMod := map[string][]diag.Diagnostic{}
	for _, d := range ds {
		mn := fileMod[d.File]
		if mn == "" {
			mn = mainName(mods)
		}
		perMod[mn] = append(perMod[mn], d)
	}
	// interface hashes need the resolved modules; keys need imports' hashes,
	// so modules are stored in dependency order
	// the interface hash also records whether the module had diagnostics:
	// a full check does not report dependents of a module that failed to
	// resolve, so dependents must be re-keyed when that state changes
	ifaceHash := map[string]string{}
	for _, m := range mods {
		if rm := prog.Modules[m.name]; rm != nil {
			h := sha256.New()
			h.Write(iface.Interface(rm, srcBytes))
			_, _ = fmt.Fprintf(h, "\nerrors=%v\n", len(perMod[m.name]) > 0)
			ifaceHash[m.name] = hex.EncodeToString(h.Sum(nil))
		}
	}
	for _, m := range mods {
		m.key = ""
	}
	var rekey func(m *module) string
	rekey = func(m *module) string {
		if m.key != "" {
			return m.key
		}
		h := sha256.New()
		_, _ = fmt.Fprintf(h, "%s|%s|%s|%s|%s|%s|%s|%v|", CompilerVersion, diag.SchemaVersion, iface.Version, cfg.Target, cfg.Opt, cfg.Env, m.srcHash, m.main)
		for _, imp := range m.imports {
			im := byName[imp]
			if im == nil {
				return "" // unresolvable import: the module is not cached
			}
			if visiting[m.name] {
				return ""
			}
			visiting[m.name] = true
			k := rekey(im)
			delete(visiting, m.name)
			if k == "" {
				return ""
			}
			_, _ = fmt.Fprintf(h, "%s=%s|", imp, ifaceHash[imp])
		}
		m.key = hex.EncodeToString(h.Sum(nil))
		return m.key
	}
	for _, m := range mods {
		k := rekey(m)
		st.Rechecked = append(st.Rechecked, m.name)
		if k == "" || ifaceHash[m.name] == "" {
			continue
		}
		mds := perMod[m.name]
		if mds == nil {
			mds = []diag.Diagnostic{}
		}
		if err := store(dir, &entry{Key: k, Iface: ifaceHash[m.name], Diags: mds}); err != nil {
			return nil, st, err
		}
	}
	sort.Strings(st.Rechecked)
	st.Ifaces = ifaceHash
	if ds == nil {
		ds = []diag.Diagnostic{}
	}
	return ds, st, nil
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

func mainName(mods []*module) string {
	for _, m := range mods {
		if m.main {
			return m.name
		}
	}
	if len(mods) > 0 {
		return mods[len(mods)-1].name
	}
	return ""
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
