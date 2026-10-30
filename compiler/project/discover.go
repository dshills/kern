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
func Load(cwd string, named []string) Result {
	return LoadWith(cwd, named, Options{Discover: true})
}

// Options selects what LoadWith does.
type Options struct {
	Discover bool // add the files of every imported module (Load)
	Tests    bool // the test-file rule of `kern test` (LoadTests)
	// Overlay, when not nil, supplies a file's text in place of the
	// disk's: it is asked with the file's identity, its cleaned
	// absolute path, before every file read. The manifest and the
	// directory listings are always read from disk
	// (specs/language-server/SPEC.md §2).
	Overlay func(abs string) ([]byte, bool)
}

// LoadWith is the loader the other entry points call: Load is
// Options{Discover: true}, LoadTests adds Tests, and Entries is the
// zero Options.
func LoadWith(cwd string, named []string, o Options) Result {
	return load(cwd, named, o)
}

// LoadTests is Load for `kern test` (specs/test-command/SPEC.md §2.1): it
// also loads the test files (TestFile) of every module directory discovery
// enters, and those of each entry file's directory that declare the entry
// file's module, which join the entry files.
func LoadTests(cwd string, named []string) Result {
	return LoadWith(cwd, named, Options{Discover: true, Tests: true})
}

// Entries is Load without discovery: the root, the manifest and the entry
// files only, for a command that works on the named files alone (format).
func Entries(cwd string, named []string) Result { return LoadWith(cwd, named, Options{}) }

// TestFile reports whether a file name is a test file's: its base name
// ends in _test.kern. Discovery leaves test files out of a module
// directory in every command but `kern test`; a named file is loaded
// whatever its name.
func TestFile(name string) bool { return strings.HasSuffix(filepath.Base(name), "_test.kern") }

func load(cwd string, named []string, o Options) Result {
	discover, tests := o.Discover, o.Tests
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
		f, err := readFile(cwd, name, o)
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
	if tests {
		more, ds := entryTests(cwd, r.Files, seen, o)
		r.Files = append(r.Files, more...)
		if len(ds) > 0 {
			r.Diags = append(r.Diags, ds...)
			return r
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
			names, msg := moduleFiles(cwd, dir, tests)
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
				src, err := readFile(cwd, name, o)
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

// entryTests returns the test files of the entry files' directories that
// declare the module of an entry file in that directory and are not in
// the set yet, in directory then byte order. A test file whose prologue
// does not parse is included, so that its error is reported rather than
// the file silently skipped; a directory that cannot be listed or a test
// file that cannot be read is an API_FILE_NOT_FOUND diagnostic, for the
// same reason.
func entryTests(cwd string, entries []*source.File, seen map[string]*source.File, o Options) ([]*source.File, []diag.Diagnostic) {
	modules := map[string]map[string]bool{} // directory -> modules its entry files declare
	var dirs []string
	for _, f := range entries {
		pro, ds := parser.ParsePrologue(f)
		if len(ds) > 0 {
			continue
		}
		dir := filepath.Dir(f.Name)
		if modules[dir] == nil {
			modules[dir] = map[string]bool{}
			dirs = append(dirs, dir)
		}
		modules[dir][pro.Module] = true
	}
	var out []*source.File
	var ds []diag.Diagnostic
	for _, dir := range dirs {
		listing, err := os.ReadDir(onDisk(cwd, dir))
		if err != nil {
			ds = append(ds, diag.New("API_FILE_NOT_FOUND", dir, diag.Span{}, "cannot list %s for its test files: %v", dir, err))
			continue
		}
		for _, e := range listing {
			name := filepath.Join(dir, e.Name())
			if dir == "." {
				name = e.Name()
			}
			if !TestFile(name) || seen[identity(cwd, name)] != nil {
				continue
			}
			st, err := os.Stat(onDisk(cwd, name))
			if err != nil {
				ds = append(ds, diag.New("API_FILE_NOT_FOUND", name, diag.Span{}, "cannot read test file %s: %v", name, err))
				continue
			}
			if !st.Mode().IsRegular() {
				continue // a directory or a device named like a test file
			}
			src, err := readFile(cwd, name, o)
			if err != nil {
				ds = append(ds, diag.New("API_FILE_NOT_FOUND", name, diag.Span{}, "cannot read test file %s: %v", name, err))
				continue
			}
			src.Name = name
			if pro, ds := parser.ParsePrologue(src); len(ds) == 0 && !modules[dir][pro.Module] {
				continue // another program's test file in the same directory
			}
			seen[identity(cwd, name)] = src
			out = append(out, src)
		}
	}
	return out, ds
}

// moduleFiles lists a module directory's files (spec §2): the regular
// files named *.kern directly in it, test files only when tests is set, as
// display names in byte order; or the reason there are none.
func moduleFiles(cwd, dir string, tests bool) ([]string, string) {
	entries, err := os.ReadDir(onDisk(cwd, dir))
	if err != nil {
		return nil, err.Error()
	}
	var names []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".kern") || !tests && TestFile(e.Name()) {
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

// readFile loads a file by display name: from the overlay when it holds
// the file's identity, else from disk. The name is kept as the display
// name.
func readFile(cwd, name string, o Options) (*source.File, error) {
	if o.Overlay != nil {
		if b, ok := o.Overlay(identity(cwd, name)); ok {
			return &source.File{Name: name, Bytes: b}, nil
		}
	}
	f, err := source.Load(onDisk(cwd, name))
	if err != nil {
		return nil, err
	}
	f.Name = name
	return f, nil
}
