// Package lsp is kern's language server (specs/language-server/SPEC.md):
// the Language Server Protocol 3.17 over standard input and output,
// serving the machine API's diagnostics, format, structured fixes and
// explain-ownership to an editor.
package lsp

import "unicode/utf8"

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

// normalize clamps off to the text, moves it back to a character
// boundary, and moves the inside of a \r\n to its \r (spec R8).
func normalize(text []byte, off int, b []bool) int {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	for !b[off] {
		off--
	}
	if crlfInterior(text, off) {
		off--
	}
	return off
}

// ToPosition converts a byte offset of text to a position (spec R8).
func ToPosition(text []byte, off int, enc Encoding) Position {
	off = normalize(text, off, boundaries(text))
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
		ch += units(r, n, enc)
		i += n
	}
	return Position{Line: line, Character: ch}
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
// that character's start.
func ToOffset(text []byte, p Position, enc Encoding) int {
	if p.Line < 0 {
		return 0
	}
	start, end, ok := lineBounds(text, p.Line)
	if !ok {
		return len(text)
	}
	ch := 0
	for i := start; i < end; {
		if ch >= p.Character {
			return i
		}
		r, n := utf8.DecodeRune(text[i:])
		u := units(r, n, enc)
		if ch+u > p.Character {
			return i
		}
		ch += u
		i += n
	}
	return end
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
