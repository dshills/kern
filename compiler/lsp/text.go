// Package lsp is kern's language server (specs/language-server/SPEC.md):
// the Language Server Protocol 3.17 over standard input and output,
// serving the machine API's diagnostics, format, structured fixes and
// explain-ownership to an editor.
package lsp

import (
	"bytes"
	"slices"
	"unicode/utf8"
)

// Position is an LSP position: a 0-based line and a character offset in
// the negotiated encoding.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is an LSP range, end exclusive.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// TextEdit replaces a range of a document's text.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// Encoding is the unit an LSP character offset counts (spec §3.1).
type Encoding int

const (
	UTF16 Encoding = iota // the protocol's default
	UTF8
)

// String is the encoding's LSP name.
func (e Encoding) String() string {
	if e == UTF8 {
		return "utf-8"
	}
	return "utf-16"
}

// boundaries reports, for every offset 0..len(text), whether it is a
// character boundary: where a step of decoding text from its start with
// utf8.DecodeRune begins, or the end. An invalid byte decodes as a
// one-byte character, so it has a boundary on each side.
func boundaries(text []byte) []bool {
	b := make([]bool, len(text)+1)
	for i := 0; i < len(text); {
		b[i] = true
		_, n := utf8.DecodeRune(text[i:])
		i += n
	}
	b[len(text)] = true
	return b
}

// units is the length of one decoded character in an encoding: its bytes
// in UTF-8, its UTF-16 code units otherwise; an invalid byte is one unit
// in both (spec R8).
func units(r rune, size int, enc Encoding) int {
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

// crlfInterior reports whether off is between the \r and the \n of a \r\n.
func crlfInterior(text []byte, off int) bool {
	return off > 0 && off < len(text) && text[off-1] == '\r' && text[off] == '\n'
}

// A position is found from the start of its line alone (spec R8). A line
// starts at offset 0, after every \n, and after every \r that no \n
// follows. Each start is a character boundary of the text, because a
// byte below 0x80 is never part of a longer character, whatever bytes
// are before it; so the characters of a line are the same decoded from
// the line's start as decoded from the start of the text, and a
// conversion reads one line.
// ToPosition and ToOffset find the line by scanning the text up to it;
// a lineTable finds it by binary search.

// settle clamps off to the text and moves the inside of a \r\n to its
// \r. (An offset inside a longer character is moved to the character's
// start by column; such an offset is never before a \n, so the two
// moves do not meet.)
func settle(text []byte, off int) int {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	if crlfInterior(text, off) {
		off--
	}
	return off
}

// column is the character offset of off in the line that starts at
// start, start <= off <= len(text): the units of the characters that end
// at or before off, so that an offset inside a character is the
// character's start.
func column(text []byte, start, off int, enc Encoding) int {
	ch := 0
	for i := start; i < off; {
		r, n := utf8.DecodeRune(text[i:])
		if i+n > off {
			break
		}
		ch += units(r, n, enc)
		i += n
	}
	return ch
}

// offsetIn is the offset of a character offset in the line text[start:end]:
// the line's end when the line is shorter, and a character's start when
// the offset is inside it.
func offsetIn(text []byte, start, end, character int, enc Encoding) int {
	ch := 0
	for i := start; i < end; {
		if ch >= character {
			return i
		}
		r, n := utf8.DecodeRune(text[i:])
		u := units(r, n, enc)
		if ch+u > character {
			return i
		}
		ch += u
		i += n
	}
	return end
}

// ToPosition converts a byte offset of text to a position (spec R8): an
// offset outside the text is its nearest end, one inside a character is
// the character's start, and one inside a \r\n is its \r. It reads the
// text up to the offset; a lineTable answers the same without.
func ToPosition(text []byte, off int, enc Encoding) Position {
	off = settle(text, off)
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
	return Position{Line: line, Character: column(text, start, off, enc)}
}

// lineBounds returns the start of a 0-based line and the offset of its
// line break (or the end of the text); ok is false when the text has
// fewer lines.
func lineBounds(text []byte, line int) (start, end int, ok bool) {
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

// ToOffset converts a position to a byte offset of text (spec R8): a line
// past the last is the end of the text, a character past its line's end
// is the line's end, and a character inside a multi-unit character is
// that character's start. It reads the text up to the position's line; a
// lineTable answers the same without.
func ToOffset(text []byte, p Position, enc Encoding) int {
	if p.Line < 0 {
		return 0
	}
	start, end, ok := lineBounds(text, p.Line)
	if !ok {
		return len(text)
	}
	return offsetIn(text, start, end, p.Character, enc)
}

// lineTable is a text with the offsets at which its lines start: the
// conversions of ToPosition and ToOffset, with the same answers, in the
// time of a binary search over the lines and of reading one line. It is
// built by one pass over the text and holds the text, which must not
// change under it.
type lineTable struct {
	text   []byte
	starts []int // ascending, starts[0] == 0; one for every line
}

// newLineTable builds the line table of text.
func newLineTable(text []byte) *lineTable {
	starts := make([]int, 1, bytes.Count(text, []byte{'\n'})+1)
	for i, c := range text {
		if c == '\n' || c == '\r' && (i+1 == len(text) || text[i+1] != '\n') {
			starts = append(starts, i+1)
		}
	}
	return &lineTable{text: text, starts: starts}
}

// position is ToPosition(t.text, off, enc).
func (t *lineTable) position(off int, enc Encoding) Position {
	off = settle(t.text, off)
	// the line of off is the last that starts at or before it
	line, found := slices.BinarySearch(t.starts, off)
	if !found {
		line--
	}
	return Position{Line: line, Character: column(t.text, t.starts[line], off, enc)}
}

// offset is ToOffset(t.text, p, enc).
func (t *lineTable) offset(p Position, enc Encoding) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(t.starts) {
		return len(t.text)
	}
	start, end := t.starts[p.Line], len(t.text)
	if p.Line+1 < len(t.starts) {
		// the line ends at its break, which ends where the next line
		// starts: a \n, a \r, or a \r\n
		end = t.starts[p.Line+1] - 1
		if t.text[end] == '\n' && end > start && t.text[end-1] == '\r' {
			end--
		}
	}
	return offsetIn(t.text, start, end, p.Character, enc)
}

// rng is the range of the span start..end of the text.
func (t *lineTable) rng(start, end int, enc Encoding) Range {
	return Range{Start: t.position(start, enc), End: t.position(end, enc)}
}

// MinimalEdit is the one edit turning old into new (spec R9): the common
// prefix at a character boundary of both texts, then the common suffix
// of what remains, likewise, then neither end inside a \r\n of old. It
// returns false when the texts are equal.
func MinimalEdit(old, new []byte, enc Encoding) (TextEdit, bool) {
	if string(old) == string(new) {
		return TextEdit{}, false
	}
	bo, bn := boundaries(old), boundaries(new)
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	for !bo[p] || !bn[p] {
		p--
	}
	s := 0
	for s < len(old)-p && s < len(new)-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	for !bo[len(old)-s] || !bn[len(new)-s] {
		s--
	}
	if crlfInterior(old, p) {
		p--
	}
	if s > 0 && crlfInterior(old, len(old)-s) {
		s--
	}
	return TextEdit{
		Range:   Range{Start: ToPosition(old, p, enc), End: ToPosition(old, len(old)-s, enc)},
		NewText: string(new[p : len(new)-s]),
	}, true
}
