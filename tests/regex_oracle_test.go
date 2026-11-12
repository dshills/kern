package tests

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// regexNode is one node of a generated pattern: the tree the generator
// builds, which is written out as the pattern and sampled for texts the
// pattern is likely to match.
type regexNode struct {
	kind  string       // lit set dot bol eol group cat alt rep empty
	text  string       // lit: the byte; set: the bracket expression; rep: the operator
	in    [3]bool      // set: whether a, b and c are matched
	kids  []*regexNode // group, rep: one; cat, alt: several
	lo    int          // rep: the least number of iterations
	hi    int          // rep: the greatest, -1 for no limit
	empty bool         // the node can match the empty string
}

// regexMaxAtoms is the most atoms a generated pattern has, a group being
// an atom as in the grammar (specs/std-regex/SPEC.md §2, T4).
const regexMaxAtoms = 12

// regexMaxText is the greatest length of a generated text (T4).
const regexMaxText = 24

// regexGen generates patterns in the common syntax of
// specs/std-regex/SPEC.md (glossary) and texts for them, as T4 lists
// them. It never writes an escape, a byte other than a, b, c and the
// operators, a class other than [:alpha:] or an interval count above 3;
// it applies a repetition only to an atom, and only to one that cannot
// match the empty string.
type regexGen struct {
	r     *rand.Rand
	atoms int // the atoms the pattern being written may still take
}

// regexDepth is how deep a generated pattern nests its groups.
const regexDepth = 3

func (g *regexGen) pattern() *regexNode {
	g.atoms = 1 + g.r.Intn(regexMaxAtoms)
	if g.r.Intn(50) == 0 {
		g.atoms = 0
	}
	return g.alt(regexDepth)
}

func (g *regexGen) alt(depth int) *regexNode {
	k := 1
	for k < 4 && g.r.Intn(100) < 30 {
		k++
	}
	if k == 1 {
		return g.cat(depth, depth == regexDepth)
	}
	n := &regexNode{kind: "alt"}
	for range k {
		b := g.cat(depth, false)
		n.kids = append(n.kids, b)
		n.empty = n.empty || b.empty
	}
	return n
}

// cat is a branch: one to eight pieces for a branch of the pattern itself,
// none to three inside a group, and one time in twelve the empty branch
// (never when whole is set: the branch is then the whole pattern, and the
// empty pattern comes from a pattern given no atoms); fewer when the
// pattern's atoms run out.
func (g *regexGen) cat(depth int, whole bool) *regexNode {
	k := g.r.Intn(4)
	if depth == regexDepth {
		k = 1 + g.r.Intn(8)
	}
	if !whole && g.r.Intn(12) == 0 {
		k = 0
	}
	n := &regexNode{kind: "cat", empty: true}
	for i := 0; i < k && g.atoms > 0; i++ {
		p := g.piece(depth)
		n.kids = append(n.kids, p)
		n.empty = n.empty && p.empty
	}
	if len(n.kids) == 0 {
		return &regexNode{kind: "empty", empty: true}
	}
	return n
}

// piece is an atom and, four times in ten, one repetition of it: none
// when the atom can match the empty string (the common syntax's clause
// d), and never a second one (clause c).
func (g *regexGen) piece(depth int) *regexNode {
	a := g.atom(depth)
	if a.empty || g.r.Intn(100) >= 40 {
		return a
	}
	n := &regexNode{kind: "rep", kids: []*regexNode{a}}
	switch r := g.r.Intn(100); {
	case r < 20:
		n.text, n.lo, n.hi = "*", 0, -1
	case r < 35:
		n.text, n.lo, n.hi = "+", 1, -1
	case r < 50:
		n.text, n.lo, n.hi = "?", 0, 1
	case r < 65:
		k := g.r.Intn(4)
		n.text, n.lo, n.hi = fmt.Sprintf("{%d}", k), k, k
	case r < 80:
		k := g.r.Intn(4)
		n.text, n.lo, n.hi = fmt.Sprintf("{%d,}", k), k, -1
	default:
		lo := g.r.Intn(4)
		hi := lo + g.r.Intn(4-lo)
		n.text, n.lo, n.hi = fmt.Sprintf("{%d,%d}", lo, hi), lo, hi
	}
	n.empty = n.lo == 0
	return n
}

func (g *regexGen) atom(depth int) *regexNode {
	g.atoms--
	switch r := g.r.Intn(100); {
	case r < 45 || (r >= 68 && depth == 0):
		return &regexNode{kind: "lit", text: string("abc"[g.r.Intn(3)])}
	case r < 57:
		return g.bracket()
	case r < 62:
		return &regexNode{kind: "dot"}
	case r < 65:
		return &regexNode{kind: "bol", empty: true}
	case r < 68:
		return &regexNode{kind: "eol", empty: true}
	}
	in := g.alt(depth - 1)
	return &regexNode{kind: "group", kids: []*regexNode{in}, empty: in.empty}
}

// bracket is [[:alpha:]], or one to three members, each one of a, b, c or
// a range over them, negated one time in three. A `-` is written only
// inside a range, so no range is directly followed by one (which the
// oracle refuses).
func (g *regexGen) bracket() *regexNode {
	n := &regexNode{kind: "set"}
	if g.r.Intn(6) == 0 {
		n.text, n.in = "[[:alpha:]]", [3]bool{true, true, true}
		return n
	}
	var b strings.Builder
	b.WriteByte('[')
	neg := g.r.Intn(3) == 0
	if neg {
		b.WriteByte('^')
	}
	for range 1 + g.r.Intn(3) {
		lo := g.r.Intn(3)
		hi := lo
		if g.r.Intn(3) == 0 {
			hi = lo + g.r.Intn(3-lo)
			fmt.Fprintf(&b, "%c-%c", "abc"[lo], "abc"[hi])
		} else {
			b.WriteByte("abc"[lo])
		}
		for c := lo; c <= hi; c++ {
			n.in[c] = true
		}
	}
	b.WriteByte(']')
	if neg {
		for c := range n.in {
			n.in[c] = !n.in[c]
		}
	}
	n.text = b.String()
	return n
}

// write appends the pattern the node stands for.
func (n *regexNode) write(b *strings.Builder) {
	switch n.kind {
	case "lit", "set":
		b.WriteString(n.text)
	case "dot":
		b.WriteByte('.')
	case "bol":
		b.WriteByte('^')
	case "eol":
		b.WriteByte('$')
	case "group":
		b.WriteByte('(')
		n.kids[0].write(b)
		b.WriteByte(')')
	case "cat":
		for _, k := range n.kids {
			k.write(b)
		}
	case "alt":
		for i, k := range n.kids {
			if i > 0 {
				b.WriteByte('|')
			}
			k.write(b)
		}
	case "rep":
		n.kids[0].write(b)
		b.WriteString(n.text)
	}
}

// some is a random byte of a text: a, b or c, and when lines is set the
// byte 10 one time in four.
func (g *regexGen) some(lines bool) byte {
	if lines && g.r.Intn(4) == 0 {
		return '\n'
	}
	return "abc"[g.r.Intn(3)]
}

// sample appends a string the node matches when its anchors hold. With
// lines set an anchor in the middle sometimes gets the byte 10 it needs.
func (g *regexGen) sample(n *regexNode, lines bool, b *[]byte) {
	switch n.kind {
	case "lit":
		*b = append(*b, n.text[0])
	case "dot":
		*b = append(*b, "abc"[g.r.Intn(3)])
	case "set":
		// a member among a, b, c; a set with none of them matches
		// nothing in these texts, and any byte will do
		c := g.r.Intn(3)
		for i := 0; i < 3 && !n.in[c]; i++ {
			c = (c + 1) % 3
		}
		*b = append(*b, "abc"[c])
	case "bol":
		if lines && len(*b) > 0 && g.r.Intn(2) == 0 {
			*b = append(*b, '\n')
		}
	case "eol":
		if lines && g.r.Intn(2) == 0 {
			*b = append(*b, '\n')
		}
	case "group":
		g.sample(n.kids[0], lines, b)
	case "cat":
		for _, k := range n.kids {
			g.sample(k, lines, b)
		}
	case "alt":
		g.sample(n.kids[g.r.Intn(len(n.kids))], lines, b)
	case "rep":
		hi := n.hi
		if hi < 0 {
			hi = n.lo + 2
		}
		for range n.lo + g.r.Intn(hi-n.lo+1) {
			g.sample(n.kids[0], lines, b)
		}
	}
}

// textFor is a text of 0 to regexMaxText bytes over a, b, c and (in half
// of the texts) the byte 10: one time in four random bytes, otherwise one
// or two samples of the pattern with a few random bytes around them, a
// byte of it changed one time in five.
func (g *regexGen) textFor(tree *regexNode) []byte {
	lines := g.r.Intn(2) == 0
	var b []byte
	fill := func(n int) {
		for range n {
			b = append(b, g.some(lines))
		}
	}
	if g.r.Intn(4) == 0 {
		fill(g.r.Intn(regexMaxText + 1))
	} else {
		fill(g.r.Intn(4))
		g.sample(tree, lines, &b)
		if g.r.Intn(2) == 0 {
			fill(g.r.Intn(3))
			g.sample(tree, lines, &b)
		}
		fill(g.r.Intn(4))
		if len(b) > 0 && g.r.Intn(5) == 0 {
			b[g.r.Intn(len(b))] = g.some(lines)
		}
	}
	if len(b) > regexMaxText {
		b = b[:regexMaxText]
	}
	return b
}

// regexPair is one generated pattern and text.
type regexPair struct {
	pattern string
	text    []byte
}

// regexPairs generates n pairs from a seed.
func regexPairs(seed int64, n int) []regexPair {
	g := &regexGen{r: rand.New(rand.NewSource(seed))}
	pairs := make([]regexPair, 0, n)
	for range n {
		tree := g.pattern()
		var b strings.Builder
		tree.write(&b)
		pairs = append(pairs, regexPair{pattern: b.String(), text: g.textFor(tree)})
	}
	return pairs
}

// regexSyntax reads a pattern by the grammar of specs/std-regex/SPEC.md §2
// and refuses whatever is outside the common syntax (glossary, clauses a
// to g) or outside what T4 lists. It knows nothing of the generator: it
// is the check that the generator writes what it is meant to.
type regexSyntax struct {
	p     string
	i     int
	atoms int
}

// regexPart is what regexSyntax knows of the stretch it has read.
type regexPart struct {
	empty   bool // it can match the empty string
	product int  // the greatest product of interval counts around an atom of it
}

// regexCommonSyntax reports why a pattern is not one T4's generator may
// write, nil when it is: clauses a to g of the common syntax (clause b in
// the stronger form "no backslash", since T4 lists no escape), the atoms
// T4 lists and no more than regexMaxAtoms of them. Clause h is the
// oracle's to check.
func regexCommonSyntax(p string) error {
	c := &regexSyntax{p: p}
	if _, err := c.alt(); err != nil {
		return err
	}
	if c.i < len(p) {
		return fmt.Errorf("`)` at %d closes no group", c.i)
	}
	if c.atoms > regexMaxAtoms {
		return fmt.Errorf("%d atoms, more than %d", c.atoms, regexMaxAtoms)
	}
	return nil
}

func (c *regexSyntax) alt() (regexPart, error) {
	out := regexPart{product: 1}
	for {
		b, err := c.branch()
		if err != nil {
			return out, err
		}
		out.empty = out.empty || b.empty
		out.product = max(out.product, b.product)
		if c.i == len(c.p) || c.p[c.i] != '|' {
			return out, nil
		}
		c.i++
	}
}

func (c *regexSyntax) branch() (regexPart, error) {
	out := regexPart{empty: true, product: 1}
	for c.i < len(c.p) && c.p[c.i] != '|' && c.p[c.i] != ')' {
		p, err := c.piece()
		if err != nil {
			return out, err
		}
		out.empty = out.empty && p.empty
		out.product = max(out.product, p.product)
	}
	return out, nil
}

// repeat reads a repetition operator at the current byte: whether there
// is one, its least count and the greatest count written in it (0 for
// `*`, `+` and `?`, which are not intervals).
func (c *regexSyntax) repeat() (found bool, lo, count int, err error) {
	if c.i == len(c.p) {
		return false, 0, 0, nil
	}
	switch c.p[c.i] {
	case '*', '?':
		c.i++
		return true, 0, 0, nil
	case '+':
		c.i++
		return true, 1, 0, nil
	case '{':
	default:
		return false, 0, 0, nil
	}
	// clause e: {n}, {n,} or {n,m}, each count one digit from 0 to 3
	at := c.i
	digit := func() (int, bool) {
		if c.i < len(c.p) && c.p[c.i] >= '0' && c.p[c.i] <= '3' {
			c.i++
			return int(c.p[c.i-1] - '0'), true
		}
		return 0, false
	}
	is := func(b byte) bool {
		if c.i < len(c.p) && c.p[c.i] == b {
			c.i++
			return true
		}
		return false
	}
	c.i++
	n, ok := digit()
	if !ok {
		return false, 0, 0, fmt.Errorf("clause e: `{` at %d does not begin an interval with one-digit counts from 0 to 3", at)
	}
	if is('}') {
		return true, n, n, nil
	}
	if !is(',') {
		return false, 0, 0, fmt.Errorf("clause e: the interval at %d has a count of more than one digit, or is not an interval", at)
	}
	if is('}') {
		return true, n, n, nil
	}
	m, ok := digit()
	if !ok || !is('}') {
		return false, 0, 0, fmt.Errorf("clause e: the interval at %d has a count that is not one digit from 0 to 3", at)
	}
	if n > m {
		return false, 0, 0, fmt.Errorf("the interval at %d has its counts in the wrong order", at)
	}
	return true, n, m, nil
}

// piece reads an atom and the repetition after it, if there is one, and
// applies clauses c, d and g. For clause g an interval counts by the
// greatest count written in it (n for {n,}, as the oracle counts it), a
// count of 0 as 1.
func (c *regexSyntax) piece() (regexPart, error) {
	a, err := c.atom()
	if err != nil {
		return a, err
	}
	at := c.i
	found, lo, count, err := c.repeat()
	if err != nil || !found {
		return a, err
	}
	if a.empty {
		return a, fmt.Errorf("clause d: the repetition at %d is applied to an operand that matches the empty string", at)
	}
	if c.i < len(c.p) && strings.IndexByte("*+?{", c.p[c.i]) >= 0 {
		return a, fmt.Errorf("clause c: the repetition at %d is applied to a repetition's result", c.i)
	}
	a.empty = lo == 0
	a.product *= max(count, 1)
	if a.product > 1000 {
		return a, fmt.Errorf("clause g: the intervals around an atom multiply to %d at %d", a.product, at)
	}
	return a, nil
}

func (c *regexSyntax) atom() (regexPart, error) {
	c.atoms++
	at := c.i
	switch ch := c.p[c.i]; ch {
	case 'a', 'b', 'c', '.':
		c.i++
		return regexPart{product: 1}, nil
	case '^', '$':
		c.i++
		return regexPart{empty: true, product: 1}, nil
	case '(':
		c.i++
		in, err := c.alt()
		if err != nil {
			return in, err
		}
		if c.i == len(c.p) {
			return in, fmt.Errorf("the group at %d is not closed", at)
		}
		c.i++ // the `)`: alt stops at nothing else before the end
		return in, nil
	case '[':
		return regexPart{product: 1}, c.bracket()
	case '\\':
		return regexPart{}, fmt.Errorf("clause b: a backslash at %d (T4 lists no escape)", at)
	case '*', '+', '?':
		return regexPart{}, fmt.Errorf("the repetition at %d has nothing to repeat", at)
	default:
		if ch >= 128 {
			return regexPart{}, fmt.Errorf("clause a: the byte %d at %d", ch, at)
		}
		return regexPart{}, fmt.Errorf("the byte %q at %d is not one T4 lists", ch, at)
	}
}

// bracket reads [[:alpha:]], or a set of bytes and ranges over a, b, c,
// negated or not. T4 lists the class by itself, so one beside other
// members, or negated, is refused here though the common syntax has it.
func (c *regexSyntax) bracket() error {
	at := c.i
	if strings.HasPrefix(c.p[c.i:], "[[:alpha:]]") {
		c.i += len("[[:alpha:]]")
		return nil
	}
	member := func() (byte, bool) {
		if c.i < len(c.p) && c.p[c.i] >= 'a' && c.p[c.i] <= 'c' {
			c.i++
			return c.p[c.i-1], true
		}
		return 0, false
	}
	c.i++
	if c.i < len(c.p) && c.p[c.i] == '^' {
		c.i++
	}
	for n := 0; ; n++ {
		if c.i < len(c.p) && c.p[c.i] == ']' && n > 0 {
			c.i++
			return nil
		}
		if strings.HasPrefix(c.p[c.i:], "[:") {
			return fmt.Errorf("clause f: the bracket expression at %d names a class other than a lone [:alpha:]", at)
		}
		lo, ok := member()
		if !ok {
			return fmt.Errorf("the bracket expression at %d has a member that is not a, b or c, or is not closed", at)
		}
		if c.i < len(c.p) && c.p[c.i] == '-' {
			c.i++
			if hi, ok := member(); !ok || hi < lo {
				return fmt.Errorf("the bracket expression at %d has a range that is not one over a, b, c", at)
			}
		}
	}
}

// regexOutside are patterns regexCommonSyntax must refuse, one or more
// for each thing it checks: a checker that accepted them would let a
// generator defect through.
var regexOutside = []string{
	"a\xc3\xa9",          // a: a byte of 128 or more
	`a\.`, `[\]a]`, `\n`, // b: an escape
	"a**", "a+?", "a{2}*", "a?{2}", "(a)*+", // c: a repetition of a repetition
	"(a*)*", "(a|)+", "^*", "$?", "(^)+", "()*", "(a?b{0})+", "(a{0,2}){2}", "((a*))?", "(b|a?)*", // d: an operand that matches the empty string
	"a{4}", "a{01}", "a{1,4}", "a{10}", "a{1,02}", "a{,2}", "a{}", "a{1", "a{2,1}", // e: counts that are not one digit from 0 to 3
	"[[:digit:]]", "[a[:alpha:]]", "[^[:alpha:]]", "[[:alpha:]a]", "[[:alpha:]", // f: classes
	"(((((((a{3}){3}){3}){3}){3}){3}){3}){3}", // g: 3^8 is above 1000
	"d", "a b", "[d]", "[a-]", "[]a]", "[c-a]", "[a-b-c]", "[]", "[a", // not what T4 lists
	"a{", "a}", "]", "{1}", "*a", "a|*", "(a", "a)", "(", // not a pattern of this shape at all
	"aaaaaaaaaaaaa", "((((((((((((a))))))))))))", // 13 atoms
}

// regexInside are patterns regexCommonSyntax must accept.
var regexInside = []string{
	"", "|", "()", "a|", "(|a)b", "^a$", "a*", "a+", "a?", "a{0}", "a{3}", "a{0,}", "a{3,}", "a{0,0}", "a{1,3}", "a{3,3}",
	"(a*)", "(a*)b", "(a+)+", "((a)|b)+", "(a|ab)(c|bcc)(c*)", "[abc]", "[^a]", "[a-c]", "[^a-bc]", "[a-ab-bc-c]", "[[:alpha:]]", "[[:alpha:]]{2,}",
	"a.c", "^$", "$^", "a^b", "(^a|b$)+", "(((((a{3}){3}){3}){3}){3}){3}", "aaaaaaaaaaaa", "(((((((((((a)))))))))))",
}

// regexSpans writes the matches of one cursor run as the program prints
// them: each match as group 0's span and then every group's, `-` for a
// group that took no part.
func regexSpans(tag string, matches [][]int) string {
	var b strings.Builder
	b.WriteString(tag)
	b.WriteByte('m')
	for _, m := range matches {
		for i := 0; i < len(m); i += 2 {
			if i == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteByte(',')
			}
			if m[i] < 0 {
				b.WriteByte('-')
			} else {
				fmt.Fprintf(&b, "%d-%d", m[i], m[i+1])
			}
		}
	}
	return b.String()
}

// regexWant is one line the program is to print, and the pair it is for.
type regexWant struct {
	pair int
	line string
}

// regexOracleLines appends the lines the oracle gives for a pair, from
// its matches (FindAllSubmatchIndex(text, -1)): the matches and the
// replacement under the tag n (the pattern compiled with newline set)
// and, for a text with no byte 10, the same two under the tag p (the
// pattern compiled by compile, which reads such a text the same way).
func regexOracleLines(want []regexWant, at int, text []byte, matches [][]int) []regexWant {
	tags := []string{"n", "p"}
	if bytes.IndexByte(text, '\n') >= 0 {
		tags = tags[:1]
	}
	for _, tag := range tags {
		want = append(want, regexWant{at, regexSpans(tag, matches)})
	}
	return want
}

// regexInput is the program's input for the pairs: a line each, the flag
// (1 when the text holds a byte 10, 0 when it does not), the pattern in
// hex and the text in hex, a space between.
func regexInput(pairs []regexPair) []byte {
	var b bytes.Buffer
	for _, p := range pairs {
		flag := '0'
		if bytes.IndexByte(p.text, '\n') >= 0 {
			flag = '1'
		}
		fmt.Fprintf(&b, "%c %s %s\n", flag, hex.EncodeToString([]byte(p.pattern)), hex.EncodeToString(p.text))
	}
	return b.Bytes()
}

// regexOracleProgram is the kern side of TestRegexOracle. It reads lines
// as regexInput writes them, from standard input or from the file its
// first argument names, and prints for each, with the pattern compiled
// with newline set: a line `nm` and every match a cursor yields through
// next_captures, each as ` s-e` for the match and `,s-e` or `,-` for each
// group in turn; and a line `nr`, the number replace_all returned and what
// it appended, in hex. For a line whose flag is 0 it prints the same two
// lines again, as `pm` and `pr`, with the pattern compiled by compile. A
// pattern that does not compile gives its two lines as `error KIND at N`.
//
// What T4 leaves open is settled here as follows. One Scratch serves
// every search of a run, so a pair's lines also depend on a Scratch being
// good for any number of patterns in turn (R11, R17). The groups slice has
// groups() + 1 elements, all None before a pattern's first search. The
// replacement line carries the number replace_all returns, which R15
// makes the number of matches. A cursor that yields more matches than a
// text has room for (len(text) + 1) is cut off with the word `runaway`,
// so that a defect in next_captures ends as a difference and not as a
// test that never ends. And the pairs may come from a file instead of
// standard input, for the run in the interpreter (regexOracleInterp).
const regexOracleProgram = `module t
import "std/io"
import "std/str"
import "std/regex"

// hex_value is the value of a hexadecimal digit, 16 for any other byte.
func hex_value(c: u8) -> u8 {
    if c >= 48u8 && c <= 57u8 { return c - 48u8 }
    if c >= 97u8 && c <= 102u8 { return c - 87u8 }
    return 16u8
}

// unhex appends the bytes s encodes, two digits each, to dst; false when
// s is not that.
func unhex(s: view[]u8, dst: edit[Vec[u8]]) -> bool {
    if len(s) % 2usize != 0usize { return false }
    var i: usize = 0usize
    while i < len(s) {
        hi := hex_value(s[i])
        lo := hex_value(s[i + 1usize])
        if hi > 15u8 || lo > 15u8 { return false }
        dst.push(hi * 16u8 + lo)
        i = i + 2usize
    }
    return true
}

// hex_digit is the digit of a value below 16.
func hex_digit(v: u8) -> u8 {
    if v < 10u8 { return 48u8 + v }
    return 87u8 + v
}

// print_hex prints s, two digits a byte.
func print_hex(s: view[]u8) {
    digits := Vec[u8].new()
    var i: usize = 0usize
    while i < len(s) {
        digits.push(hex_digit(s[i] >> 4u32))
        digits.push(hex_digit(s[i] & 15u8))
        i = i + 1usize
    }
    print_str(digits.as_view())
}

func print_kind(k: regex.ErrKind) {
    match k {
        UnclosedGroup => print_str("UnclosedGroup")
        UnmatchedClose => print_str("UnmatchedClose")
        UnclosedBracket => print_str("UnclosedBracket")
        BadRange => print_str("BadRange")
        BadClass => print_str("BadClass")
        BadEscape => print_str("BadEscape")
        BadInterval => print_str("BadInterval")
        NothingToRepeat => print_str("NothingToRepeat")
        Unsupported => print_str("Unsupported")
        TooDeep => print_str("TooDeep")
        TooLarge => print_str("TooLarge")
    }
}

// Work is what a run keeps between pairs: the Scratch every search of the
// run uses, the group spans of the last match and the replaced text.
struct Work nocopy {
    sc: regex.Scratch
    groups: Vec[Option[regex.Span]]
    dst: Vec[u8]
}

// matches prints the line of a cursor run to its end. A text of n bytes
// has at most n + 1 matches that do not overlap, so a cursor that yields
// more is cut off and the line says so.
func matches(tag: view[]u8, re: view[regex.Regex], text: view[]u8, w: edit[Work]) {
    w.groups.clear()
    w.groups.resize(re.groups() + 1usize, None)
    cur := regex.cursor()
    print_str(tag)
    print_str("m")
    var count: usize = 0usize
    while true {
        var more: bool = false
        with g := w.groups.as_edit() { more = re.next_captures(w.sc, text, cur, g) }
        if !more { break }
        count = count + 1usize
        if count > len(text) + 1usize {
            print_str(" runaway")
            break
        }
        var i: usize = 0usize
        while i < w.groups.len() {
            if i == 0usize { print_str(" ") } else { print_str(",") }
            g := w.groups.get(i)
            match g {
                Some(s) => {
                    print_u64(u64(s.start))
                    print_str("-")
                    print_u64(u64(s.end))
                }
                None => { print_str("-") }
            }
            i = i + 1usize
        }
    }
    println()
}

// report prints the two lines of one compilation of a pair's pattern, the
// error twice when the pattern was refused.
func report(tag: view[]u8, r: view[Result[regex.Regex, regex.CompileErr]], text: view[]u8, w: edit[Work]) {
    match r {
        Ok(re) => {
            matches(tag, re, text, w)
        }
        Err(e) => {
            var i: usize = 0usize
            while i < 1usize {
                print_str(tag)
                if i == 0usize { print_str("m") } else { print_str("r") }
                print_str(" error ")
                print_kind(e.kind)
                print_str(" at ")
                print_u64(u64(e.at))
                println()
                i = i + 1usize
            }
        }
    }
}

// pair runs one line: the flag, a space, the pattern in hex, a space, the
// text in hex. The flag is 1 when the text holds a byte 10. It prints two
// lines for the pattern compiled with newline set and, when the flag is
// 0, two more for the pattern compiled by compile.
func pair(line: view[]u8, pattern: edit[Vec[u8]], text: edit[Vec[u8]], w: edit[Work]) {
    n := len(line)
    var cut: usize = n
    if n >= 2usize {
        match str.find_byte(line[2usize:n], 32u8) {
            Some(k) => { cut = 2usize + k }
            None => { }
        }
    }
    pattern.clear()
    text.clear()
    var ok: bool = cut < n && line[1usize] == 32u8 && (line[0usize] == 48u8 || line[0usize] == 49u8)
    if ok { ok = unhex(line[2usize:cut], pattern) }
    if ok { ok = unhex(line[cut + 1usize:n], text) }
    if !ok {
        print_str("bad line")
        println()
        return
    }
    o := regex.options()
    o.newline = true
    with r := regex.compile_with(pattern.as_view(), o) { report("n", r, text.as_view(), w) }
    if line[0usize] == 48u8 {
        with r := regex.compile(pattern.as_view()) { report("p", r, text.as_view(), w) }
    }
}

// feed runs every line of r.
func feed[R: io.Reader](r: edit[io.BufReader[R]]) -> Result[(), io.IoErr] {
    w := Work {
        sc: regex.Scratch.new(),
        groups: Vec[Option[regex.Span]].new(),
        dst: Vec[u8].new(),
    }
    line := Vec[u8].new()
    pattern := Vec[u8].new()
    text := Vec[u8].new()
    while true {
        line.clear()
        if !r.read_line(line)? { break }
        pair(line.as_view(), pattern, text, w)
    }
    return Ok(())
}

// run reads the pairs from standard input, or from the file the first
// argument names when there is one.
func run() -> Result[(), io.IoErr] {
    args := io.args()
    if args.len() > 1usize {
        with path := args.get(1usize) {
            r := io.BufReader[io.File].new(io.open_read(path.as_view())?)
            feed[io.File](r)?
        }
        return Ok(())
    }
    r := io.BufReader[io.Stdin].new(io.stdin())
    feed[io.Stdin](r)?
    return Ok(())
}

func main() -> i32 {
    match run() {
        Ok(u) => { return 0i32 }
        Err(e) => {
            print_str("input failed")
            println()
        }
    }
    return 1i32
}
`

// regexOracleName is the file name the program is compiled under.
const regexOracleName = "regex_oracle.kern"

// regexOracleLimit is how long the native program may take over all the
// pairs of a run.
const regexOracleLimit = 10 * time.Minute

// regexOracleBuild builds the program natively at release and returns the
// executable.
func regexOracleBuild(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "regex_oracle")
	files := []*source.File{{Name: regexOracleName, Bytes: []byte(regexOracleProgram)}}
	ds, err := native.Build(files, exe, native.Options{Opt: "release"})
	if err != nil {
		t.Fatalf("native build: %v", err)
	}
	if len(ds) > 0 {
		t.Fatalf("the program does not compile: %s", render(ds))
	}
	return exe
}

// regexOracleNative feeds the input to the executable in one run and
// returns what it printed.
func regexOracleNative(t *testing.T, exe string, input []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), regexOracleLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Stdin = bytes.NewReader(input)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Errorf("the native program did not finish in %s", regexOracleLimit)
	case errors.As(err, &ee):
		// what it printed before it stopped is still compared
		t.Errorf("the native program ended with %v; stderr %.2000q", err, errb.Bytes())
	case err != nil:
		t.Fatalf("native run: %v", err)
	}
	return out.Bytes()
}

// regexOracleInterp runs the program on the input in the interpreter, in
// this process, and returns what it printed. The interpreter's standard
// input is the test binary's own, so the input goes through a file the
// program is given as its argument.
func regexOracleInterp(t *testing.T, input []byte) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pairs.txt")
	if err := os.WriteFile(path, input, 0o644); err != nil {
		t.Fatal(err)
	}
	files := []*source.File{{Name: regexOracleName, Bytes: []byte(regexOracleProgram)}}
	prog := resolve.Resolve(files)
	if len(prog.Diags) > 0 {
		t.Fatalf("the program does not resolve: %s", render(prog.Diags))
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("the program does not check: %s", render(ds))
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", render(ds))
	}
	if err := opt.Run(mod, opt.Options{}); err != nil {
		t.Fatalf("opt: %v", err)
	}
	var out, errb bytes.Buffer
	it := interp.New(mod, regexOracleName, &out, &errb)
	it.SetArgs([]string{regexOracleName, path})
	if status := it.Run(); status != 0 {
		// what it printed before it stopped is still compared
		t.Errorf("interpreter status %d; stderr %.2000q", status, errb.Bytes())
	}
	return out.Bytes()
}

// regexMaxReported is how many differing lines a comparison reports in
// full before it only counts them.
const regexMaxReported = 10

// regexShown is a line as a difference shows it: a replacement line is
// followed by the bytes its hex stands for.
func regexShown(line string) string {
	if f := strings.Fields(line); len(f) == 3 && strings.HasSuffix(f[0], "r") {
		if b, err := hex.DecodeString(f[2]); err == nil {
			return fmt.Sprintf("%s (%q)", line, b)
		}
	}
	return line
}

// regexCompare compares what the program printed with the lines wanted,
// line by line, and reports the first differences with their pairs. A
// line of another kind than the one due (the program stopped, or printed
// a line too many or too few) ends the comparison: every line after it
// would differ for that reason alone.
func regexCompare(t *testing.T, pairs []regexPair, want []regexWant, out []byte) {
	t.Helper()
	var got []string
	if len(out) > 0 {
		got = strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	}
	lines, inPairs, last := 0, 0, -1
	for i, w := range want {
		p := pairs[w.pair]
		if i == len(got) {
			t.Errorf("the program printed %d of %d lines: it stopped at pair %d, pattern %q, text %q", len(got), len(want), w.pair, p.pattern, p.text)
			break
		}
		if got[i] == w.line {
			continue
		}
		if !strings.HasPrefix(got[i], w.line[:2]) {
			t.Errorf("line %d, pair %d, pattern %q, text %q: the program printed %q where its %q line was due; the lines after it are out of step and are not compared", i+1, w.pair, p.pattern, p.text, got[i], w.line[:2])
			break
		}
		lines++
		if w.pair != last {
			last = w.pair
			inPairs++
		}
		if lines <= regexMaxReported {
			t.Errorf("pair %d, pattern %q, text %q:\n  oracle %s\n  kern   %s", w.pair, p.pattern, p.text, regexShown(w.line), regexShown(got[i]))
		}
	}
	if len(got) > len(want) {
		t.Errorf("the program printed %d lines, %d more than the pairs call for; the first of them is %q", len(got), len(got)-len(want), got[len(want)])
	}
	if lines > 0 {
		t.Errorf("%d of %d lines differ, in %d pairs (the first %d are shown); one Scratch serves a whole run, so a pair's lines may also depend on the pairs before it", lines, len(want), inPairs, min(lines, regexMaxReported))
	}
}

// TestRegexOracle compares std/regex with the oracle of
// specs/std-regex/SPEC.md, Go's regexp.CompilePOSIX, on generated pairs
// of a pattern and a text (T4; R13, R14, R15). From seed 1 it makes
// 20,000 pairs (KERN_REGEX_PAIRS overrides the number): patterns in the
// common syntax of up to 12 atoms over a, b, c, with groups, alternation,
// bracket expressions, `.`, anchors, `*`, `+`, `?` and intervals; texts
// of 0 to 24 bytes over a, b, c and the byte 10. One kern program
// (regexOracleProgram) prints, for each pair, every match a cursor
// yields with all its group spans and what replace_all with the template
// `<&|\1>` gives, the pattern compiled with newline set and, for a text
// with no byte 10, compiled by compile as well. The oracle's lines are
// written from FindAllSubmatchIndex(text, -1), the replacement built
// from those matches by R15. The program is built natively at release
// and fed every pair in one run, and every tenth pair is run in the
// interpreter. No difference is permitted.
//
// Before either run the test checks its own generator: every pattern is
// read by regexCommonSyntax, which refuses what is outside the common
// syntax, and compiled by the oracle. A pattern the oracle refuses is a
// defect of the generator and fails the test.
func TestRegexOracle(t *testing.T) {
	n := 20000
	if s := os.Getenv("KERN_REGEX_PAIRS"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			t.Fatalf("KERN_REGEX_PAIRS=%q: want a positive integer", s)
		}
		n = v
	}

	// the checker first: it must refuse what it is there to refuse
	for _, p := range regexOutside {
		if err := regexCommonSyntax(p); err == nil {
			t.Fatalf("regexCommonSyntax accepts %q", p)
		}
	}
	for _, p := range regexInside {
		if err := regexCommonSyntax(p); err != nil {
			t.Fatalf("regexCommonSyntax refuses %q: %v", p, err)
		}
		if _, err := regexp.CompilePOSIX(p); err != nil {
			t.Fatalf("the oracle refuses %q: %v", p, err)
		}
	}

	pairs := regexPairs(1, n)
	var want []regexWant
	// how many pairs are of each kind, and the least in 100 that must be
	// (about half of what seed 1 gives)
	cover := []struct {
		what  string
		least int
		have  int
	}{
		{what: "have a match other than one empty match", least: 40},
		{what: "have several matches", least: 30},
		{what: "have a match with a group's span", least: 24},
		{what: "have a match with a group that took no part", least: 14},
		{what: "have a text with a byte 10", least: 17},
		{what: "have a text with no byte 10, compared with compile as well", least: 33},
	}
	for i, p := range pairs {
		if err := regexCommonSyntax(p.pattern); err != nil {
			t.Fatalf("generator defect: pair %d, pattern %q is outside the common syntax: %v", i, p.pattern, err)
		}
		re, err := regexp.CompilePOSIX(p.pattern)
		if err != nil {
			t.Fatalf("generator defect: pair %d, the oracle refuses the pattern %q: %v", i, p.pattern, err)
		}
		if len(p.text) > regexMaxText || len(bytes.Trim(p.text, "abc\n")) > 0 {
			t.Fatalf("generator defect: pair %d, text %q", i, p.text)
		}
		ms := re.FindAllSubmatchIndex(p.text, -1)
		want = regexOracleLines(want, i, p.text, ms)

		// what the pairs cover, by the oracle's matches
		var some, none bool
		for _, m := range ms {
			for g := 2; g < len(m); g += 2 {
				some = some || m[g] >= 0
				none = none || m[g] < 0
			}
		}
		for k, on := range []bool{
			len(ms) > 1 || (len(ms) == 1 && ms[0][1] > ms[0][0]),
			len(ms) > 1,
			some,
			none,
			bytes.IndexByte(p.text, '\n') >= 0,
			bytes.IndexByte(p.text, '\n') < 0,
		} {
			if on {
				cover[k].have++
			}
		}
	}
	t.Logf("%d pairs, %d lines", n, len(want))
	for _, c := range cover {
		t.Logf("%6d pairs %s", c.have, c.what)
		// a generator that no longer wrote what T4 is for would pass
		// every comparison
		if n >= 1000 && c.have*100 < c.least*n {
			t.Fatalf("generator defect: %d of %d pairs %s; at least %d in 100 are due", c.have, n, c.what, c.least)
		}
	}

	t.Run("native", func(t *testing.T) {
		if !native.Available() {
			t.Skip("clang not available")
		}
		exe := regexOracleBuild(t)
		regexCompare(t, pairs, want, regexOracleNative(t, exe, regexInput(pairs)))
	})

	t.Run("interpreter", func(t *testing.T) {
		// every tenth pair, with the lines wanted for it
		var tenth []regexPair
		for i := 0; i < len(pairs); i += 10 {
			tenth = append(tenth, pairs[i])
		}
		var wantTenth []regexWant
		for _, w := range want {
			if w.pair%10 == 0 {
				wantTenth = append(wantTenth, w)
			}
		}
		regexCompare(t, pairs, wantTenth, regexOracleInterp(t, regexInput(tenth)))
	})
}
