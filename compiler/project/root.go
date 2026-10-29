package project

import (
	"os"
	"path/filepath"
)

// FindRoot determines the project root (spec §4.1): the nearest ancestor
// directory of the first named file, or of the working directory when no
// file is named, that holds a regular file named kern.mod; else, with a
// named file, the working directory. Relative names are accessed under
// cwd and the root is spelled relative to cwd when the first named file
// is relative, absolute when it is absolute; an ancestor that cannot be
// inspected counts as not holding a manifest. The manifest's name is
// returned with found true; found false with a named file means the
// fallback root (spelled "."), and with no named file means no root.
func FindRoot(cwd string, named []string) (root string, manifest string, found bool) {
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs // a relative working directory would stop the walk at "."
	}
	start := "."
	if len(named) > 0 {
		start = filepath.Dir(named[0])
	}
	abs := start
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, start)
	}
	spell := func(dir string) string {
		if len(named) > 0 && filepath.IsAbs(named[0]) {
			return dir
		}
		if rel, err := filepath.Rel(cwd, dir); err == nil {
			return rel
		}
		return dir
	}
	for dir := abs; ; {
		if st, err := os.Stat(filepath.Join(dir, ManifestName)); err == nil && st.Mode().IsRegular() {
			r := spell(dir)
			return r, filepath.Join(r, ManifestName), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if len(named) > 0 {
		return ".", "", false
	}
	return "", "", false
}
