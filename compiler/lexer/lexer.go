package lexer

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Error is a lexical error at a byte offset.
type Error struct {
	Start, End int
	Msg        string
}

func (e Error) Error() string { return fmt.Sprintf("%d: %s", e.Start, e.Msg) }

// Lex tokenizes src. Newline tokens are emitted only where a statement may
// end (after an identifier, literal, closing bracket, '?', or one of the
// keywords return/break/continue/true/false/self), following Go's rule, so
// expressions may continue across lines after operators and commas.
func Lex(src []byte) ([]Token, []Error) {
	l := &lexer{src: src}
	l.run()
	return l.toks, l.errs
}

// Comment is one `//` line comment. Text is the source text from the marker
// to the end of the line, excluding the line terminator. OwnLine reports that
// only whitespace precedes the comment on its line.
type Comment struct {
	Start, End int
	Text       string
	OwnLine    bool
}

// LexWithComments is Lex, additionally returning every comment in source
// order. Fixture header lines (`//!`) are returned as comments too. The
// tokens and errors are identical to those of Lex.
func LexWithComments(src []byte) ([]Token, []Comment, []Error) {
	l := &lexer{src: src, keepComments: true}
	l.run()
	return l.toks, l.comments, l.errs
}

type lexer struct {
	src          []byte
	pos          int
	toks         []Token
	errs         []Error
	keepComments bool
	comments     []Comment
}

func (l *lexer) emit(k Kind, start, end int, text string) {
	l.toks = append(l.toks, Token{Kind: k, Text: text, Start: start, End: end})
}

func (l *lexer) lastAllowsNewline() bool {
	if len(l.toks) == 0 {
		return false
	}
	switch l.toks[len(l.toks)-1].Kind {
	case Ident, Int, Float, String, RParen, RBrack, RBrace, Question,
		KwReturn, KwBreak, KwContinue, KwTrue, KwFalse, KwSelf, Underscore:
		return true
	}
	return false
}

func (l *lexer) run() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			if l.lastAllowsNewline() {
				l.emit(Newline, l.pos, l.pos+1, "\n")
			}
			l.pos++
		case c == ' ' || c == '\t' || c == '\r':
			l.pos++
		case c == '/' && l.peek(1) == '/':
			start := l.pos
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			if l.keepComments {
				end := l.pos
				if end > start && l.src[end-1] == '\r' {
					end--
				}
				l.comments = append(l.comments, Comment{Start: start, End: end, Text: string(l.src[start:end]), OwnLine: l.ownLine(start)})
			}
		case c == '"':
			l.lexString()
		case isDigit(c):
			l.lexNumber()
		case c == '_' || isLetter(c):
			l.lexIdent()
		default:
			l.lexPunct()
		}
	}
	if l.lastAllowsNewline() {
		l.emit(Newline, l.pos, l.pos, "\n")
	}
	l.emit(EOF, l.pos, l.pos, "")
}

// ownLine reports whether only spaces and tabs precede offset start on its line.
func (l *lexer) ownLine(start int) bool {
	for i := start - 1; i >= 0; i-- {
		switch l.src[i] {
		case ' ', '\t', '\r':
			continue
		case '\n':
			return true
		default:
			return false
		}
	}
	return true
}

func (l *lexer) peek(n int) byte {
	if l.pos+n < len(l.src) {
		return l.src[l.pos+n]
	}
	return 0
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 }

func (l *lexer) lexIdent() {
	start := l.pos
	if c := l.src[l.pos]; c >= 0x80 {
		if r, _ := utf8.DecodeRune(l.src[l.pos:]); !unicode.IsLetter(r) {
			l.lexPunct() // a leading non-letter rune (digit, symbol, invalid byte)
			return
		}
	}
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '_' || isDigit(c) || isLetter(c) {
			if c >= 0x80 {
				r, n := utf8.DecodeRune(l.src[l.pos:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					break
				}
				l.pos += n
				continue
			}
			l.pos++
			continue
		}
		break
	}
	if l.pos == start {
		// a non-ASCII byte that is not a letter: report and advance
		l.lexPunct()
		return
	}
	text := string(l.src[start:l.pos])
	if k, ok := keywords[text]; ok {
		l.emit(k, start, l.pos, text)
		return
	}
	l.emit(Ident, start, l.pos, text)
}

var primSuffixes = []string{"isize", "usize", "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64"}

func (l *lexer) lexNumber() {
	start := l.pos
	isFloat := false
	for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' && isDigit(l.peek(1)) {
		isFloat = true
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
	}
	for _, s := range primSuffixes {
		if bytes.HasPrefix(l.src[l.pos:], []byte(s)) {
			after := l.pos + len(s)
			if after < len(l.src) && (isLetter(l.src[after]) || isDigit(l.src[after]) || l.src[after] == '_') {
				continue
			}
			l.pos = after
			break
		}
	}
	k := Int
	if isFloat {
		k = Float
	}
	l.emit(k, start, l.pos, string(l.src[start:l.pos]))
}

func (l *lexer) lexString() {
	start := l.pos
	l.pos++ // opening quote
	var sb strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '"':
			l.pos++
			l.emit(String, start, l.pos, sb.String())
			return
		case '\\':
			if l.pos+1 >= len(l.src) {
				l.pos++
				continue
			}
			l.pos++
			switch l.src[l.pos] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case '0':
				sb.WriteByte(0)
			default:
				l.errs = append(l.errs, Error{l.pos - 1, l.pos + 1, "unknown escape"})
			}
			l.pos++
		case '\n':
			l.errs = append(l.errs, Error{start, l.pos, "unterminated string"})
			l.emit(String, start, l.pos, sb.String())
			return
		default:
			sb.WriteByte(c)
			l.pos++
		}
	}
	l.errs = append(l.errs, Error{start, l.pos, "unterminated string"})
	l.emit(String, start, l.pos, sb.String())
}

var puncts = []struct {
	s string
	k Kind
}{
	{"..", DotDot}, {":=", ColonEq}, {"->", Arrow}, {"=>", FatArrow}, {"==", Eq}, {"!=", Ne},
	{"<=", Le}, {">=", Ge}, {"<<", Shl}, {">>", Shr}, {"&&", AndAnd}, {"||", OrOr},
	{"(", LParen}, {")", RParen}, {"[", LBrack}, {"]", RBrack}, {"{", LBrace}, {"}", RBrace},
	{",", Comma}, {".", Dot}, {":", Colon}, {";", Semi}, {"=", Assign}, {"<", Lt}, {">", Gt},
	{"+", Plus}, {"-", Minus}, {"*", Star}, {"/", Slash}, {"%", Percent}, {"&", Amp},
	{"|", Pipe}, {"^", Caret}, {"!", Bang}, {"?", Question},
}

func (l *lexer) lexPunct() {
	rest := l.src[l.pos:]
	for _, p := range puncts {
		if len(rest) >= len(p.s) && string(rest[:len(p.s)]) == p.s {
			l.emit(p.k, l.pos, l.pos+len(p.s), p.s)
			l.pos += len(p.s)
			return
		}
	}
	_, n := utf8.DecodeRune(rest)
	l.errs = append(l.errs, Error{l.pos, l.pos + n, fmt.Sprintf("unexpected character %q", string(rest[:n]))})
	l.pos += n
}
