package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

// Result is what Load found.
type Result struct {
	Root     string    // the project root, spelled as the search found it ("." for the fallback)
	Manifest *Manifest // nil when no kern.mod was found
	// Files is the discovered file set in spec order: the entry files in
	// the order given, then the discovered files in the order discovery
	// added them. Entry is how many of them are entry files.
	Files []*source.File
	Entry int
	// Diags is every failure of the manifest, the entry files and
	// discovery; the set is complete when it is empty.
	Diags []diag.Diagnostic
}

// Names returns the display names of the files, in order.
func (r Result) Names() []string {
	out := make([]string, len(r.Files))
	for i, f := range r.Files {
		out[i] = f.Name
	}
	return out
}

// Load finds the root and manifest (FindRoot), the entry files (named, or
// the manifest's entry when none is named) and then every file of every
// module the set imports by a user import path (spec §4.3). Relative names
// are accessed under cwd and kept as display names.
func Load(cwd string, named []string) Result { return load(cwd, named, true) }

// Entries is Load without discovery: the root, the manifest and the entry
// files only, for a command that works on the named files alone (format).
func Entries(cwd string, named []string) Result { return load(cwd, named, false) }

func load(cwd string, named []string, discover bool) Result {
	var r Result
	root, mpath, found := FindRoot(cwd, named)
	r.Root = root
	if found {
		m, ds := loadManifest(cwd, mpath)
		if len(ds) > 0 {
			r.Diags = ds
			return r
		}
		r.Manifest = m
	}
	if len(named) == 0 {
		if !found {
			r.Diags = []diag.Diagnostic{diag.New("PROJECT_NOT_FOUND", "", diag.Span{}, "no file named and no %s in the working directory or its ancestors", ManifestName)}
			return r
		}
		if len(r.Manifest.Entry) == 0 {
			r.Diags = []diag.Diagnostic{diag.New("PROJECT_INVALID", mpath, diag.Span{}, "%s names no entry file and none was given", ManifestName)}
			return r
		}
		for _, e := range r.Manifest.Entry {
			named = append(named, filepath.Join(root, filepath.FromSlash(e)))
		}
	}
	seen := map[string]*source.File{} // the files in the set, by identity
	for _, name := range named {
		f, err := source.Load(onDisk(cwd, name))
		if err != nil {
			r.Diags = []diag.Diagnostic{diag.New("API_FILE_NOT_FOUND", name, diag.Span{}, "cannot read %s: %v", name, err)}
			return r
		}
		f.Name = name
		r.Files = append(r.Files, f)
		if seen[identity(cwd, name)] == nil {
			seen[identity(cwd, name)] = f
		}
	}
	r.Entry = len(r.Files)
	if !discover {
		return r
	}
	// breadth-first over the set: a module directory is entered once when
	// it succeeds, and a failure is reported at every import that names it
	var aliases []string // the alias each discovered file must declare
	visited := map[string]bool{}
	for i := 0; i < len(r.Files); i++ {
		f := r.Files[i]
		pro, ds := parser.ParsePrologue(f)
		if len(ds) > 0 {
			r.Diags = append(r.Diags, ds...)
			continue
		}
		if i >= r.Entry && pro.Module != aliases[i-r.Entry] {
			r.Diags = append(r.Diags, diag.New("MODULE_MISMATCH", f.Name, diag.Span{Start: pro.ModulePos.Start, End: pro.ModulePos.End}, "file declares module %q, expected %q", pro.Module, aliases[i-r.Entry]))
		}
		for _, imp := range pro.Imports {
			if strings.HasPrefix(imp.Path, "std/") {
				continue
			}
			at := diag.Span{Start: imp.Start, End: imp.End}
			if !ValidImportPath(imp.Path) {
				r.Diags = append(r.Diags, diag.New("MODULE_NOT_FOUND", f.Name, at, "import path %q is not a relative module path", imp.Path))
				continue
			}
			dir := filepath.Join(root, filepath.FromSlash(imp.Path))
			key := identity(cwd, dir)
			if visited[key] {
				continue
			}
			names, msg := moduleFiles(cwd, dir)
			if msg != "" {
				r.Diags = append(r.Diags, diag.New("MODULE_NOT_FOUND", f.Name, at, "import %q: %s", imp.Path, msg))
				continue
			}
			alias := imp.Path[strings.LastIndex(imp.Path, "/")+1:]
			var loaded []*source.File
			var already []*source.File // files of the directory that are in the set already
			failed := false
			for _, name := range names {
				if have := seen[identity(cwd, name)]; have != nil {
					already = append(already, have)
					continue
				}
				src, err := source.Load(onDisk(cwd, name))
				if err != nil {
					r.Diags = append(r.Diags, diag.New("MODULE_NOT_FOUND", f.Name, at, "import %q: cannot read %s: %v", imp.Path, name, err))
					failed = true
					break
				}
				src.Name = name
				loaded = append(loaded, src)
			}
			if failed {
				continue
			}
			visited[key] = true
			// every file of the module directory must declare the alias:
			// the ones already in the set (named, or found through another
			// path) are checked here, the new ones when the loop reaches them
			for _, have := range already {
				if pro, ds := parser.ParsePrologue(have); len(ds) == 0 && pro.Module != alias {
					r.Diags = append(r.Diags, diag.New("MODULE_MISMATCH", have.Name, diag.Span{Start: pro.ModulePos.Start, End: pro.ModulePos.End}, "file declares module %q, expected %q", pro.Module, alias))
				}
			}
			for _, src := range loaded {
				seen[identity(cwd, src.Name)] = src
				r.Files = append(r.Files, src)
				aliases = append(aliases, alias)
			}
		}
	}
	return r
}

// ValidImportPath reports whether p is a user import path (spec §2): one
// or more "/"-separated components, none empty, "." or "..", none holding
// "\", ":" or a control byte (below 0x20, or 0x7f). A "std/" path is not one.
func ValidImportPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "std/") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			return false
		}
		for i := 0; i < len(c); i++ {
			if c[i] == '\\' || c[i] == ':' || c[i] < 0x20 || c[i] == 0x7f {
				return false
			}
		}
	}
	return true
}

// moduleFiles lists a module directory's files (spec §2): the regular
// files named *.kern directly in it, as display names in byte order; or
// the reason there are none.
func moduleFiles(cwd, dir string) ([]string, string) {
	entries, err := os.ReadDir(onDisk(cwd, dir))
	if err != nil {
		return nil, err.Error()
	}
	var names []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".kern") {
			continue
		}
		if st, err := os.Stat(filepath.Join(onDisk(cwd, dir), e.Name())); err != nil || !st.Mode().IsRegular() {
			continue
		}
		names = append(names, filepath.Join(dir, e.Name()))
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, "no .kern file in " + dir
	}
	return names, ""
}

// onDisk is where a display name is accessed: itself when absolute, else
// under cwd.
func onDisk(cwd, name string) string {
	if filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(cwd, name)
}

// identity is the file identity of a display name (spec §2): its cleaned
// absolute form; no symbolic link is resolved.
func identity(cwd, name string) string { return filepath.Clean(onDisk(cwd, name)) }
