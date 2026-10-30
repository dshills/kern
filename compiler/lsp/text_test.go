package lsp

import (
	"testing"
)

func TestToPosition(t *testing.T) {
	// "aé€😀\r\nb\rc\nd": a(1) é(2) €(3) 😀(4) \r\n b \r c \n d
	text := []byte("aé€😀\r\nb\rc\nd")
	cases := []struct {
		off     int
		u8, u16 Position
		comment string
	}{
		{0, Position{0, 0}, Position{0, 0}, "start"},
		{1, Position{0, 1}, Position{0, 1}, "after ASCII"},
		{3, Position{0, 3}, Position{0, 2}, "after two-byte"},
		{2, Position{0, 1}, Position{0, 1}, "inside two-byte: its start"},
		{6, Position{0, 6}, Position{0, 3}, "after three-byte"},
		{10, Position{0, 10}, Position{0, 5}, "after four-byte: two UTF-16 units"},
		{8, Position{0, 6}, Position{0, 3}, "inside four-byte"},
		{11, Position{0, 10}, Position{0, 5}, "inside \\r\\n: the \\r"},
		{12, Position{1, 0}, Position{1, 0}, "after \\r\\n"},
		{14, Position{2, 0}, Position{2, 0}, "after lone \\r"},
		{16, Position{3, 0}, Position{3, 0}, "after \\n"},
		{17, Position{3, 1}, Position{3, 1}, "end"},
		{99, Position{3, 1}, Position{3, 1}, "past the end"},
		{-3, Position{0, 0}, Position{0, 0}, "negative"},
	}
	for _, c := range cases {
		if got := ToPosition(text, c.off, UTF8); got != c.u8 {
			t.Errorf("%s: utf-8 ToPosition(%d) = %v, want %v", c.comment, c.off, got, c.u8)
		}
		if got := ToPosition(text, c.off, UTF16); got != c.u16 {
			t.Errorf("%s: utf-16 ToPosition(%d) = %v, want %v", c.comment, c.off, got, c.u16)
		}
	}
}

func TestToOffset(t *testing.T) {
	text := []byte("aé€😀\r\nb\rc\nd")
	cases := []struct {
		p       Position
		enc     Encoding
		want    int
		comment string
	}{
		{Position{0, 3}, UTF8, 3, "after two-byte"},
		{Position{0, 2}, UTF8, 1, "inside two-byte (utf-8)"},
		{Position{0, 4}, UTF16, 6, "inside surrogate pair: the character's start"},
		{Position{0, 5}, UTF16, 10, "after the pair"},
		{Position{0, 99}, UTF16, 10, "past the line's end: the line's end (the \\r)"},
		{Position{1, 5}, UTF8, 13, "past a line ended by lone \\r"},
		{Position{2, 0}, UTF8, 14, "line after lone \\r"},
		{Position{3, 1}, UTF8, 17, "end"},
		{Position{4, 0}, UTF8, 17, "past the last line"},
		{Position{-1, 0}, UTF8, 0, "negative line"},
	}
	for _, c := range cases {
		if got := ToOffset(text, c.p, c.enc); got != c.want {
			t.Errorf("%s: ToOffset(%v, %v) = %d, want %d", c.comment, c.p, c.enc, got, c.want)
		}
	}
}

// TestRoundTrip: every canonical offset round-trips; every other offset
// comes back as its normalization.
func TestRoundTrip(t *testing.T) {
	texts := []string{"aé€😀\r\nb\rc\nd", "\xffx\x80\xe2\x82y", "\r\n\r\n", "", "line\n"}
	for _, s := range texts {
		text := []byte(s)
		b := boundaries(text)
		for _, enc := range []Encoding{UTF8, UTF16} {
			for off := 0; off <= len(text); off++ {
				want := normalize(text, off, b)
				if got := ToOffset(text, ToPosition(text, off, enc), enc); got != want {
					t.Errorf("%q %v: offset %d -> %v -> %d, want %d", s, enc, off, ToPosition(text, off, enc), got, want)
				}
			}
		}
	}
}

func TestInvalidUTF8(t *testing.T) {
	// \xff and a lone continuation byte are one unit each in both
	// encodings; a truncated three-byte sequence decodes byte by byte
	text := []byte("\xffx\x80\xe2\x82y")
	for _, enc := range []Encoding{UTF8, UTF16} {
		if got := ToPosition(text, 6, enc); got != (Position{0, 6}) {
			t.Errorf("%v: end = %v, want 0:6", enc, got)
		}
		if got := ToOffset(text, Position{0, 3}, enc); got != 3 {
			t.Errorf("%v: character 3 = %d, want 3", enc, got)
		}
	}
}

func apply(old []byte, e TextEdit, enc Encoding) string {
	s, en := ToOffset(old, e.Range.Start, enc), ToOffset(old, e.Range.End, enc)
	return string(old[:s]) + e.NewText + string(old[en:])
}

func TestMinimalEdit(t *testing.T) {
	cases := []struct {
		old, new string
		start    int // byte offsets of the replaced range in old
		end      int
		text     string
	}{
		{"abc", "xabc", 0, 0, "x"},
		{"abc", "abcx", 3, 3, "x"},
		{"abc", "axc", 1, 2, "x"},
		{"aaa", "aaaa", 3, 3, "a"}, // prefix first, the suffix never overlaps it
		{"é", "è", 0, 2, "è"},      // shared lead byte: the prefix stops at the boundary
		{"aé", "aéb", 3, 3, "b"},
		{"xé", "yé", 0, 1, "y"},
		{"a\nb", "a\r\nb", 1, 1, "\r"},         // inserting a \r before an existing \n
		{"a\r\nb", "a\nb", 1, 3, "\n"},         // deleting the \r: the suffix may not start inside the \r\n
		{"a\r\nb", "a\r\r\nb", 1, 3, "\r\r\n"}, // neither end may fall inside the old \r\n
		{"a\r\nb", "a\r\nxb", 3, 3, "x"},
		{"\xffa", "\xffb", 1, 2, "b"},         // an invalid byte is a character of its own
		{"\xe2\x82a", "\xe2\x82b", 2, 3, "b"}, // a truncated sequence decodes byte by byte
	}
	for _, c := range cases {
		for _, enc := range []Encoding{UTF8, UTF16} {
			e, ok := MinimalEdit([]byte(c.old), []byte(c.new), enc)
			if !ok {
				t.Fatalf("%q -> %q: no edit", c.old, c.new)
			}
			old := []byte(c.old)
			want := Range{Start: ToPosition(old, c.start, enc), End: ToPosition(old, c.end, enc)}
			if e.Range != want || e.NewText != c.text {
				t.Errorf("%q -> %q (%v): edit %v %q, want %v %q", c.old, c.new, enc, e.Range, e.NewText, want, c.text)
			}
			if got := apply(old, e, enc); got != c.new {
				t.Errorf("%q -> %q (%v): applying gives %q", c.old, c.new, enc, got)
			}
		}
	}
	if _, ok := MinimalEdit([]byte("same"), []byte("same"), UTF8); ok {
		t.Error("equal texts gave an edit")
	}
}

func TestURI(t *testing.T) {
	for _, c := range []struct {
		uri, path string
		ok        bool
	}{
		{"file:///tmp/a%20b/x.kern", "/tmp/a b/x.kern", true},
		{"file://localhost/tmp/x.kern", "/tmp/x.kern", true},
		{"file:///tmp/../tmp/./x.kern", "/tmp/x.kern", true},
		{"untitled:Untitled-1", "", false},
		{"file://host/tmp/x.kern", "", false},
		{"file:relative.kern", "", false},
	} {
		p, ok := PathOf(c.uri)
		if p != c.path || ok != c.ok {
			t.Errorf("PathOf(%q) = %q %v, want %q %v", c.uri, p, ok, c.path, c.ok)
		}
	}
	if got := URIOf("/tmp/a b/x.kern"); got != "file:///tmp/a%20b/x.kern" {
		t.Errorf("URIOf = %q", got)
	}
	if p, _ := PathOf(URIOf("/tmp/é #?.kern")); p != "/tmp/é #?.kern" {
		t.Errorf("round trip = %q", p)
	}
}
