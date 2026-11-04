package tests

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

// diagForms are the Go parser's and lexer's message forms
// (specs/selfhost-diags §1), each as a pattern over a message.
var diagForms = []struct{ name, re string }{
	{"expected %s, found %s", `^expected .*, found `},
	{"expected end of statement", `^expected end of statement, found `},
	{"expected declaration", `^expected declaration, found `},
	{"interface name expected", `^interface name expected before 'for'$`},
	{"expected invariant/requires/ensures", `^expected invariant, requires or ensures$`},
	{"budget before func", `^a budget block must precede a func declaration, found `},
	{"array length", `^array length must be a non-negative integer literal$`},
	{"expected type", `^expected type, found `},
	{"end of file in block", `^unexpected end of file in block$`},
	{"module-qualified type name", `^expected a module-qualified type name before '\{'$`},
	{"type name before brace", `^expected type name before '\{'$`},
	{"expected type argument", `^expected type argument$`},
	{"invalid float literal", `^invalid float literal$`},
	{"truncate primitive", `^truncate requires a primitive integer type$`},
	{"expected expression", `^expected expression, found `},
	{"integer out of range", `^integer literal out of range$`},
	{"unknown escape", `^unknown escape$`},
	{"unterminated string", `^unterminated string$`},
	{"unexpected character", `^unexpected character `},
}

// formOf names the form a message has; the more specific forms are tried
// before "expected %s, found %s".
func formOf(msg string) string {
	for _, f := range diagForms[1:] {
		if regexp.MustCompile(f.re).MatchString(msg) {
			return f.name
		}
	}
	if regexp.MustCompile(diagForms[0].re).MatchString(msg) {
		return diagForms[0].name
	}
	return "other: " + msg
}

// goLines renders the Go parser's diagnostics as the kern program's
// lines: `start:end message`.
func goLines(name string, src []byte, bodyless bool) (string, []string) {
	_, ds := parser.ParseWith(&source.File{Name: name, Bytes: src}, parser.Options{AllowBodyless: bodyless})
	var b strings.Builder
	var msgs []string
	for _, d := range ds {
		fmt.Fprintf(&b, "%d:%d %s\n", d.Span.Start, d.Span.End, d.Message)
		msgs = append(msgs, d.Message)
	}
	return b.String(), msgs
}

// nonASCIIOutside reports a byte at or above 0x80 outside string
// literals and comments, which the kern lexer does not support.
func nonASCIIOutside(src []byte) bool {
	for i := 0; i < len(src); i++ {
		switch c := src[i]; {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"':
			for i++; i < len(src) && src[i] != '"' && src[i] != '\n'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case c >= 0x80:
			return true
		}
	}
	return false
}

// diagCases reach the message forms the mutants rarely do.
var diagCases = map[string]string{
	"interface name expected": "module m\nimpl () for T { }\n",
	"contract keyword":        "module m\ncontract { foo \"x\" }\nfunc f() { }\n",
	"type argument int":       "module m\nfunc f() { x := A[1] { } }\n",
	"type argument field":     "module m\nfunc f() { x := A[p.q.r] { } }\n",
	"module-qualified":        "module m\nfunc f() { x := A[b.c.D[u8]] { } }\n",
	"type name before brace":  "module m\nfunc f() { x := A[g()[u8]] { } }\n",
	"unexpected tab-like":     "module m\nfunc f() { x := 1 \x01 2 }\n",
	"several lexical errors":  "module m\nfunc f() { s := \"a\\qb\" $ @ \"open\n}\n",
	// float literals at the edges of strconv.ParseFloat's range (no
	// exponent form exists): zero, leading zeros, underflow to 0, the
	// largest finite value, just above it, and far above it
	"float zero":          "module m\nconst X: f64 = 0.0\n",
	"float leading zeros": "module m\nconst X: f64 = 0007.25\n",
	"float underflow":     "module m\nconst X: f64 = 0." + strings.Repeat("0", 400) + "1\n",
	"float largest":       "module m\nconst X: f64 = 17976931348623157" + strings.Repeat("0", 292) + ".0\n",
	"float just above":    "module m\nconst X: f64 = 17976931348623159" + strings.Repeat("0", 292) + ".0\n",
	"float far above":     "module m\nconst X: f64 = 1" + strings.Repeat("0", 400) + ".0\n",
}

// mutant is one mutation of a source: its description and text.
type mutant struct {
	what string
	src  []byte
}

var (
	replaceFragments = []string{")", "}", "]", ",", ":", "=", "func", "return", "1", "x", "."}
	insertFragments  = []string{"$", "@", "#", "`", `"a\qb"`, "\"ab\n", "1" + strings.Repeat("0", 400) + ".0"}
)

// mutants makes n mutations of src from its Go tokens: a token deleted,
// duplicated or replaced, or a fragment inserted before it.
func mutants(r *rand.Rand, src []byte, n int) []mutant {
	toks, _ := lexer.Lex(src)
	var real []lexer.Token
	for _, t := range toks {
		if t.Kind != lexer.Newline && t.Kind != lexer.EOF {
			real = append(real, t)
		}
	}
	if len(real) == 0 {
		return nil
	}
	splice := func(a, b int, mid string) []byte {
		out := append([]byte{}, src[:a]...)
		out = append(out, mid...)
		return append(out, src[b:]...)
	}
	var out []mutant
	for k := 0; k < n; k++ {
		t := real[r.Intn(len(real))]
		switch r.Intn(4) {
		case 0:
			out = append(out, mutant{fmt.Sprintf("delete %d:%d", t.Start, t.End), splice(t.Start, t.End, "")})
		case 1:
			text := string(src[t.Start:t.End])
			out = append(out, mutant{fmt.Sprintf("duplicate %d:%d", t.Start, t.End), splice(t.Start, t.End, text+" "+text)})
		case 2:
			f := replaceFragments[r.Intn(len(replaceFragments))]
			out = append(out, mutant{fmt.Sprintf("replace %d:%d with %q", t.Start, t.End, f), splice(t.Start, t.End, f)})
		default:
			f := insertFragments[r.Intn(len(insertFragments))]
			out = append(out, mutant{fmt.Sprintf("insert %q at %d", f, t.Start), splice(t.Start, t.Start, f+" ")})
		}
	}
	return out
}

// With diags the kern parser prints the Go parser's diagnostics, every
// lexical error or the one syntax error, with the same spans and messages,
// for the corpus, the reject list and seeded mutants of every accepted
// corpus file (specs/selfhost-diags R1-R4).
func TestSelfhostDiags(t *testing.T) {
	if !native.Available() {
		t.Skip("clang not available")
	}
	bin := selfhostBin(t, filepath.Join("..", "examples", "selfhost_parser"))
	dir := t.TempDir()
	r := rand.New(rand.NewSource(20261104))
	var inputs, lexical, syntax, skippedCorpus, skippedMutants, n, mutantCount int
	seen := map[string]int{}
	check := func(name string, src []byte, bodyless, corpus bool) {
		if nonASCIIOutside(src) {
			if corpus {
				skippedCorpus++
			} else {
				skippedMutants++
			}
			return
		}
		inputs++
		want, msgs := goLines(name, src, bodyless)
		path := filepath.Join(dir, fmt.Sprintf("in%d.kern", n))
		n++
		if err := os.WriteFile(path, src, 0o644); err != nil {
			t.Fatal(err)
		}
		args := []string{path, "diags"}
		if bodyless {
			args = append(args, "bodyless")
		}
		out, err := exec.Command(bin, args...).Output()
		var ee *exec.ExitError
		code := 0
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := string(out); got != want || (code == 0) != (want == "") {
			t.Errorf("%s: kern (exit %d)\n%s\nGo\n%s", name, code, got, want)
			return
		}
		for _, m := range msgs {
			seen[formOf(m)]++
		}
		switch {
		case len(msgs) == 0:
		case strings.HasPrefix(formOf(msgs[0]), "unknown escape") || strings.HasPrefix(formOf(msgs[0]), "unterminated") || strings.HasPrefix(formOf(msgs[0]), "unexpected character"):
			lexical++
		default:
			syntax++
		}
	}
	for _, d := range []string{"tests", "std", "examples", filepath.Join("bench", "kern"), filepath.Join("compiler", "resolve", "prelude")} {
		bodyless := d == "std" || strings.Contains(d, "prelude")
		werr := filepath.Walk(filepath.Join("..", d), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".kern") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			check(path, src, bodyless, true)
			if _, ds := parser.ParseWith(&source.File{Name: path, Bytes: src}, parser.Options{AllowBodyless: bodyless}); len(ds) == 0 {
				for _, m := range mutants(r, src, 4) {
					mutantCount++
					check(path+" ("+m.what+")", m.src, bodyless, false)
				}
			}
			return nil
		})
		if werr != nil {
			t.Fatalf("walking %s: %v", d, werr)
		}
	}
	names := make([]string, 0, len(selfhostRejects))
	for name := range selfhostRejects {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		check("reject "+name, []byte(selfhostRejects[name]), false, false)
	}
	cases := make([]string, 0, len(diagCases))
	for name := range diagCases {
		cases = append(cases, name)
	}
	sort.Strings(cases)
	for _, name := range cases {
		check("case "+name, []byte(diagCases[name]), false, false)
	}
	var forms []string
	for f, c := range seen {
		forms = append(forms, fmt.Sprintf("%s (%d)", f, c))
	}
	sort.Strings(forms)
	t.Logf("%d inputs (%d mutants): %d with lexical errors, %d with a syntax error; skipped %d corpus files and %d mutants with non-ASCII outside strings", inputs, mutantCount, lexical, syntax, skippedCorpus, skippedMutants)
	t.Logf("message forms seen (%d): %s", len(seen), strings.Join(forms, "; "))
	if skippedCorpus > 0 {
		t.Errorf("%d corpus files skipped", skippedCorpus)
	}
	if inputs < 2000 || mutantCount < 1500 {
		t.Errorf("only %d inputs, %d mutants", inputs, mutantCount)
	}
	if len(seen) != len(diagForms) {
		t.Errorf("%d message forms seen, want all %d", len(seen), len(diagForms))
	}
}
