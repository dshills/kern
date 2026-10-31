// Package lexer turns kern source into tokens.
package lexer

// Kind is a token kind.
type Kind int

// Token kinds. Keywords and punctuation each have their own kind.
const (
	EOF Kind = iota
	Newline
	Ident
	Int
	Float
	String
	// keywords
	KwModule
	KwImport
	KwStruct
	KwUnion
	KwInterface
	KwImpl
	KwFor
	KwFunc
	KwExtern
	KwConst
	KwDestructor
	KwContract
	KwInvariant
	KwRequires
	KwEnsures
	KwView
	KwEdit
	KwOwn
	KwOut
	KwSelf
	KwVar
	KwWith
	KwMatch
	KwWhile
	KwIf
	KwElse
	KwReturn
	KwBreak
	KwContinue
	KwUnsafe
	KwAssert
	KwIn
	KwMove
	KwCopy
	KwTake
	KwReplace
	KwDrop
	KwTruncate
	KwTrue
	KwFalse
	KwNocopy
	KwThreadLocal
	// punctuation
	LParen
	RParen
	LBrack
	RBrack
	LBrace
	RBrace
	Comma
	Dot
	DotDot
	Colon
	ColonEq
	Semi
	Arrow
	FatArrow
	Assign
	Eq
	Ne
	Lt
	Le
	Gt
	Ge
	Plus
	Minus
	Star
	Slash
	Percent
	Amp
	Pipe
	Caret
	Shl
	Shr
	AndAnd
	OrOr
	Bang
	Question
	Underscore
)

var keywords = map[string]Kind{
	"module": KwModule, "import": KwImport, "struct": KwStruct, "union": KwUnion,
	"interface": KwInterface, "impl": KwImpl, "for": KwFor, "func": KwFunc,
	"extern": KwExtern, "const": KwConst, "destructor": KwDestructor,
	"contract": KwContract, "invariant": KwInvariant, "requires": KwRequires,
	"ensures": KwEnsures, "view": KwView, "edit": KwEdit,
	"own": KwOwn, "out": KwOut, "self": KwSelf, "var": KwVar, "with": KwWith,
	"match": KwMatch, "while": KwWhile, "if": KwIf, "else": KwElse,
	"return": KwReturn, "break": KwBreak, "continue": KwContinue,
	"unsafe": KwUnsafe, "assert": KwAssert, "in": KwIn, "move": KwMove,
	"copy": KwCopy, "take": KwTake, "replace": KwReplace, "drop": KwDrop,
	"truncate": KwTruncate, "true": KwTrue, "false": KwFalse,
	"nocopy": KwNocopy, "thread_local": KwThreadLocal, "_": Underscore,
}

var kindNames = map[Kind]string{
	EOF: "end of file", Newline: "newline", Ident: "identifier", Int: "integer literal",
	Float: "float literal", String: "string literal",
	LParen: "(", RParen: ")", LBrack: "[", RBrack: "]", LBrace: "{", RBrace: "}",
	Comma: ",", Dot: ".", DotDot: "..", Colon: ":", ColonEq: ":=", Semi: ";",
	Arrow: "->", FatArrow: "=>", Assign: "=", Eq: "==", Ne: "!=", Lt: "<", Le: "<=",
	Gt: ">", Ge: ">=", Plus: "+", Minus: "-", Star: "*", Slash: "/", Percent: "%",
	Amp: "&", Pipe: "|", Caret: "^", Shl: "<<", Shr: ">>", AndAnd: "&&", OrOr: "||",
	Bang: "!", Question: "?", Underscore: "_",
}

// String returns a human name for the kind.
func (k Kind) String() string {
	if s, ok := kindNames[k]; ok {
		return s
	}
	for w, kk := range keywords {
		if kk == k {
			return "'" + w + "'"
		}
	}
	return "token"
}

// Token is one lexed token. Text is the source text; for String tokens it is
// the decoded contents.
type Token struct {
	Kind  Kind
	Text  string
	Start int
	End   int
}

// IsKeyword reports whether s is a reserved word of the language.
func IsKeyword(s string) bool {
	_, ok := keywords[s]
	return ok
}
