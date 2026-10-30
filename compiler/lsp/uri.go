package lsp

import (
	"net/url"
	"path/filepath"
)

// PathOf is the cleaned absolute path of a file URI; ok is false for
// any other scheme, a host other than localhost, or a relative path.
func PathOf(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return "", false
	}
	p := filepath.FromSlash(u.Path)
	if !filepath.IsAbs(p) {
		return "", false
	}
	return filepath.Clean(p), true
}

// URIOf is the file URI of an absolute path.
func URIOf(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
