package lexer

import "testing"

func kinds(toks []Token) []Kind {
	out := make([]Kind, 0, len(toks))
	for _, t := range toks {
		out = append(out, t.Kind)
	}
	return out
}

func TestNewlineInsertion(t *testing.T) {
	src := "x := a +\n  b\ny := 1\n"
	toks, errs := Lex([]byte(src))
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	want := []Kind{Ident, ColonEq, Ident, Plus, Ident, Newline, Ident, ColonEq, Int, Newline, EOF}
	got := kinds(toks)
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestLiteralsAndSuffixes(t *testing.T) {
	toks, errs := Lex([]byte(`0u64 1_000i32 3.5f32 "a\n" view[]u8`))
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if toks[0].Text != "0u64" || toks[1].Text != "1_000i32" || toks[2].Kind != Float || toks[3].Text != "a\n" {
		t.Fatalf("unexpected tokens %+v", toks[:4])
	}
	if toks[4].Kind != KwView || toks[5].Kind != LBrack || toks[6].Kind != RBrack || toks[7].Text != "u8" {
		t.Fatalf("unexpected view tokens %+v", toks[4:8])
	}
}

func TestHeaderCommentIgnored(t *testing.T) {
	toks, _ := Lex([]byte("//! phase: 1\nmodule m\n"))
	if toks[0].Kind != KwModule {
		t.Fatalf("got %v", toks[0])
	}
}

func TestLexerTerminatesOnOddInput(t *testing.T) {
	for _, src := range []string{"x := \"abc\\", "a 😀 b", "\xff\xfe", "\"unterminated"} {
		toks, errs := Lex([]byte(src))
		if len(errs) == 0 || toks[len(toks)-1].Kind != EOF {
			t.Fatalf("%q: expected an error and EOF, got %v / %v", src, errs, toks)
		}
	}
}

func TestLexWithComments(t *testing.T) {
	src := "//! phase: 1\nmodule m\n// own line\nfunc f() { }   // trailing\t\n"
	toks, comments, errs := LexWithComments([]byte(src))
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	plain, _ := Lex([]byte(src))
	if len(plain) != len(toks) {
		t.Fatalf("token streams differ: %d vs %d", len(plain), len(toks))
	}
	for i := range plain {
		if plain[i] != toks[i] {
			t.Fatalf("token %d differs: %+v vs %+v", i, plain[i], toks[i])
		}
	}
	want := []Comment{
		{Start: 0, End: 12, Text: "//! phase: 1", OwnLine: true},
		{Start: 22, End: 33, Text: "// own line", OwnLine: true},
		{Start: 49, End: 61, Text: "// trailing\t", OwnLine: false},
	}
	if len(comments) != len(want) {
		t.Fatalf("got %d comments %+v, want %d", len(comments), comments, len(want))
	}
	for i := range want {
		if comments[i] != want[i] {
			t.Fatalf("comment %d: got %+v want %+v", i, comments[i], want[i])
		}
	}
}
