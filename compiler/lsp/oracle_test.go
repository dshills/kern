package lsp

import (
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// The oracle: position conversion as it was before the line table, kept
// word for word (the names apart, and the character boundaries of a
// text, which the old ToPosition worked out again at every call, passed
// in by a caller that asks about many offsets of one text) so that the
// table and the functions that scan are compared with it on every offset
// and every position. Its cost for one answer grows with the text;
// nothing but the tests may use it.

// oracleNormalize clamps off to the text, moves it back to a character
// boundary, and moves the inside of a \r\n to its \r (spec R8).
func oracleNormalize(text []byte, off int, b []bool) int {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	for !b[off] {
		off--
	}
	if off > 0 && off < len(text) && text[off-1] == '\r' && text[off] == '\n' {
		off--
	}
	return off
}

// oracleBoundaries reports, for every offset 0..len(text), whether a step
// of decoding text from its start begins there, or it is the end.
func oracleBoundaries(text []byte) []bool {
	b := make([]bool, len(text)+1)
	for i := 0; i < len(text); {
		b[i] = true
		_, n := utf8.DecodeRune(text[i:])
		i += n
	}
	b[len(text)] = true
	return b
}

func oracleUnits(r rune, size int, enc Encoding) int {
	if r == utf8.RuneError && size == 1 {
		return 1
	}
	if enc == UTF8 {
		return size
	}
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// oraclePositionIn is the old ToPosition, b being oracleBoundaries(text),
// which it began by computing.
func oraclePositionIn(text []byte, b []bool, off int, enc Encoding) Position {
	off = oracleNormalize(text, off, b)
	line, start := 0, 0
	for i := 0; i < off; i++ {
		switch text[i] {
		case '\n':
			line, start = line+1, i+1
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				i++ // off is never inside a \r\n, so its \n is before off
			}
			line, start = line+1, i+1
		}
	}
	ch := 0
	for i := start; i < off; {
		r, n := utf8.DecodeRune(text[i:])
		ch += oracleUnits(r, n, enc)
		i += n
	}
	return Position{Line: line, Character: ch}
}

func oracleLineBounds(text []byte, line int) (start, end int, ok bool) {
	for l := 0; l < line; l++ {
		i := start
		for i < len(text) && text[i] != '\n' && text[i] != '\r' {
			i++
		}
		if i == len(text) {
			return 0, 0, false
		}
		if text[i] == '\r' && i+1 < len(text) && text[i+1] == '\n' {
			i++
		}
		start = i + 1
	}
	end = start
	for end < len(text) && text[end] != '\n' && text[end] != '\r' {
		end++
	}
	return start, end, true
}

func oracleOffset(text []byte, p Position, enc Encoding) int {
	if p.Line < 0 {
		return 0
	}
	start, end, ok := oracleLineBounds(text, p.Line)
	if !ok {
		return len(text)
	}
	ch := 0
	for i := start; i < end; {
		if ch >= p.Character {
			return i
		}
		r, n := utf8.DecodeRune(text[i:])
		u := oracleUnits(r, n, enc)
		if ch+u > p.Character {
			return i
		}
		ch += u
		i += n
	}
	return end
}

// oracleText is one text under comparison: its boundaries for the
// oracle, and its line table.
type oracleText struct {
	text []byte
	b    []bool
	tab  *lineTable
}

func newOracleText(text []byte) *oracleText {
	return &oracleText{text: text, b: oracleBoundaries(text), tab: newLineTable(text)}
}

// lines is the number of lines the oracle counts, which the table must
// have.
func (o *oracleText) lines() (int, error) {
	n := oraclePositionIn(o.text, o.b, len(o.text), UTF8).Line + 1
	if len(o.tab.starts) != n {
		return n, fmt.Errorf("the table has %d lines, the text %d", len(o.tab.starts), n)
	}
	return n, nil
}

// offset compares the position of one byte offset, in both encodings,
// as the table and as ToPosition give it, with the oracle's.
func (o *oracleText) offset(off int) error {
	for _, enc := range []Encoding{UTF8, UTF16} {
		want := oraclePositionIn(o.text, o.b, off, enc)
		if got := o.tab.position(off, enc); got != want {
			return fmt.Errorf("%v: the table's position of offset %d is %+v, the oracle's %+v", enc, off, got, want)
		}
		if got := ToPosition(o.text, off, enc); got != want {
			return fmt.Errorf("%v: ToPosition(%d) is %+v, the oracle's %+v", enc, off, got, want)
		}
	}
	return nil
}

// position compares the offset of one position likewise.
func (o *oracleText) position(p Position) error {
	for _, enc := range []Encoding{UTF8, UTF16} {
		want := oracleOffset(o.text, p, enc)
		if got := o.tab.offset(p, enc); got != want {
			return fmt.Errorf("%v: the table's offset of %+v is %d, the oracle's %d", enc, p, got, want)
		}
		if got := ToOffset(o.text, p, enc); got != want {
			return fmt.Errorf("%v: ToOffset(%+v) is %d, the oracle's %d", enc, p, got, want)
		}
	}
	return nil
}

// matchOracle compares the conversions of one text with the oracle's at
// every byte offset from two before the text to two after it, and at
// every position from the line before the first to two lines after the
// last, the characters from -1 to two past the line's bytes (a line has
// no more units than bytes). It returns the first difference.
func matchOracle(text []byte) error {
	o := newOracleText(text)
	lines, err := o.lines()
	if err != nil {
		return err
	}
	for off := -2; off <= len(text)+2; off++ {
		if err := o.offset(off); err != nil {
			return err
		}
	}
	for line := -1; line <= lines+1; line++ {
		width := 0
		if start, end, ok := oracleLineBounds(text, line); ok {
			width = end - start
		}
		for ch := -1; ch <= width+2; ch++ {
			if err := o.position(Position{Line: line, Character: ch}); err != nil {
				return err
			}
		}
	}
	return nil
}

// sampleOracle compares the conversions of one text with the oracle's
// at n offsets spread over it and at its end, and for each at the
// positions about it: the characters on either side, past the end of
// its line, and the start of the next line.
func sampleOracle(text []byte, n int) error {
	o := newOracleText(text)
	if _, err := o.lines(); err != nil {
		return err
	}
	for k := 0; k <= n; k++ {
		off := k * len(text) / n
		if err := o.offset(off); err != nil {
			return err
		}
		p := oraclePositionIn(text, o.b, off, UTF16)
		for _, q := range []Position{
			{Line: p.Line, Character: p.Character - 1}, p, {Line: p.Line, Character: p.Character + 1},
			{Line: p.Line, Character: 1 << 30}, {Line: p.Line + 1},
		} {
			if err := o.position(q); err != nil {
				return err
			}
		}
	}
	return nil
}

// oracleSequences calls f with every sequence of at most n of the
// pieces, joined.
func oracleSequences(pieces []string, n int, f func(text string)) {
	var rec func(prefix string, left int)
	rec = func(prefix string, left int) {
		f(prefix)
		if left == 0 {
			return
		}
		for _, p := range pieces {
			rec(prefix+p, left-1)
		}
	}
	rec("", n)
}

// TestConversionMatchesOracleGenerated: every text of at most six of the
// bytes that make the rules (a letter, \r, \n, the two bytes of é, an
// invalid byte), every text of at most five whole pieces (line breaks of
// each kind, characters of one to four bytes, one outside the BMP, an
// invalid byte, a truncated sequence, a continuation byte alone), and
// longer random texts of more pieces with long and empty lines; each at
// every offset and every position.
func TestConversionMatchesOracleGenerated(t *testing.T) {
	n := 0
	check := func(text string) {
		n++
		if err := matchOracle([]byte(text)); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}
	oracleSequences([]string{"a", "\r", "\n", "\xc3", "\xa9", "\xff"}, 6, check)
	pieces := []string{"a", "\n", "\r", "\r\n", "é", "€", "😀", "\xff", "\xe2\x82", "\x80"}
	oracleSequences(pieces, 5, check)
	more := append([]string{"𝄞", "\xf0\x9f\x98", "\xed\xa0\x80", "\xc0\x80", "\t", " ", "z", "\n\n", "\r\r", "\n\r"}, pieces...)
	rng := rand.New(rand.NewSource(20261112))
	for i := 0; i < 4000; i++ {
		var sb strings.Builder
		for line, lines := 0, rng.Intn(12); line <= lines; line++ {
			// half the lines are empty, the others of up to forty pieces
			for k, width := 0, rng.Intn(40)*rng.Intn(2); k < width; k++ {
				sb.WriteString(more[rng.Intn(len(more))])
			}
			if line < lines || rng.Intn(2) == 0 {
				sb.WriteString([]string{"\n", "\r\n", "\r"}[rng.Intn(3)])
			}
		}
		check(sb.String())
	}
	t.Logf("%d texts", n)
}

// oracleWhole is the size up to which a repository file is compared at
// every offset and every position in an ordinary run. The oracle reads a
// text from its start for each answer, so a file costs the square of its
// size: all 858 files in full were 440 seconds of processor time when
// this was written, the files up to this size two.
const oracleWhole = 2 << 10

// oracleWindow is the size of the pieces of a larger file that are
// compared in full where they hold anything but ASCII and \n.
const oracleWindow = 1 << 10

// TestConversionMatchesOracleRepository: the .kern files of the
// repository. With KERN_LSP_ORACLE_ALL set, every file at every offset
// and every position. Without, that for the files up to oracleWhole
// (seven in ten); a larger file at 32 offsets spread over it with the
// positions about them, and each of its windows that holds a \r or a
// byte outside ASCII, as a text of its own, at every offset and
// position.
func TestConversionMatchesOracleRepository(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && path != root && strings.HasPrefix(e.Name(), ".") {
			return filepath.SkipDir // version control, and other checkouts under it
		}
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".kern") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 500 {
		t.Fatalf("%d .kern files under %s: the walk is wrong", len(paths), root)
	}
	all := os.Getenv("KERN_LSP_ORACLE_ALL") != ""
	unusual := func(b []byte) bool {
		for _, c := range b {
			if c >= 0x80 || c == '\r' {
				return true
			}
		}
		return false
	}
	const workers = 8
	for w := 0; w < workers; w++ {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			t.Parallel()
			whole, sampled, windows := 0, 0, 0
			// every eighth file of the walk's order, so that no worker
			// has all the large ones
			for i := w; i < len(paths); i += workers {
				text, err := os.ReadFile(paths[i])
				if err != nil {
					t.Fatal(err)
				}
				if all || len(text) <= oracleWhole {
					if err := matchOracle(text); err != nil {
						t.Fatalf("%s: %v", paths[i], err)
					}
					whole++
					continue
				}
				if err := sampleOracle(text, 32); err != nil {
					t.Fatalf("%s: %v", paths[i], err)
				}
				sampled++
				for at := 0; at < len(text); at += oracleWindow {
					piece := text[at:min(at+oracleWindow, len(text))]
					if !unusual(piece) {
						continue
					}
					if err := matchOracle(piece); err != nil {
						t.Fatalf("%s, the %d bytes from %d: %v", paths[i], len(piece), at, err)
					}
					windows++
				}
			}
			t.Logf("%d files in full, %d sampled with %d windows in full", whole, sampled, windows)
		})
	}
}

// TestDocumentTable: a document's table is built once for a text and
// replaced with the text; a request's text gets the document's table
// only when it is the document's text.
func TestDocumentTable(t *testing.T) {
	s := New(strings.NewReader(""), os.Stderr, Options{})
	d := &document{uri: "file:///w/a.kern", path: "/w/a.kern", text: []byte("ab\ncd\n")}
	s.docs[d.uri] = d
	tab := d.table()
	if d.table() != tab || s.linesOf(d.path) != tab || s.lines(d.path, []byte("ab\ncd\n")) != tab {
		t.Error("the document's table was built again for the same text")
	}
	if other := s.lines(d.path, []byte("ab\ncd\nef")); other == tab || other.position(9, UTF16) != (Position{Line: 2, Character: 2}) {
		t.Error("another text of the same path got the document's table")
	}
	d.text = []byte("ab\ncd\nef\n") // as a change does: a new slice
	next := d.table()
	if next == tab || next.position(10, UTF16) != (Position{Line: 3, Character: 0}) || s.linesOf(d.path) != next {
		t.Errorf("after a change the table is of the old text: %+v", next.starts)
	}
	d.text = nil
	if got := d.table(); got == next || got.position(5, UTF16) != (Position{}) || d.table() != got {
		t.Error("an emptied document kept its table, or builds one for each call")
	}
	// a file that is not open and cannot be read converts as an empty text
	if got := s.linesOf("/w/missing.kern").position(7, UTF16); got != (Position{}) {
		t.Errorf("a missing file's position is %+v", got)
	}
}
