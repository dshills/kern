package source

import "testing"

func TestHeadersLineBoundary(t *testing.T) {
	f := &File{Name: "x.kern", Bytes: []byte("//! phase: 1\n\n//! expect: accept\nmodule m\n//! not a header\nfunc f() { }\n")}
	h := f.Headers()
	if len(h) != 2 || h[0] != "phase: 1" || h[1] != "expect: accept" {
		t.Fatalf("headers: %v", h)
	}
	if f.Line(0) != 1 || f.Line(13) != 2 || f.Line(len(f.Bytes)) != 7 {
		t.Fatalf("lines: %d %d %d", f.Line(0), f.Line(13), f.Line(len(f.Bytes)))
	}
	u := &File{Bytes: []byte("é")}
	if !u.OnBoundary(0) || u.OnBoundary(1) || !u.OnBoundary(2) || u.OnBoundary(3) || u.OnBoundary(-1) {
		t.Fatal("utf-8 boundaries")
	}
}
