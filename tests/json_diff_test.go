package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// jsonGen generates JSON texts from a fixed seed; it writes lone
// surrogate escapes too, which Go accepts and std/json rejects, as RFC 8259
// asks of UTF-8 text.
type jsonGen struct {
	r *rand.Rand
}

func (g *jsonGen) ws(b *strings.Builder) {
	for n := g.r.Intn(3); n > 0; n-- {
		b.WriteByte(" \t\n\r"[g.r.Intn(4)])
	}
}

func (g *jsonGen) number(b *strings.Builder) {
	if g.r.Intn(3) == 0 {
		b.WriteByte('-')
	}
	if g.r.Intn(4) == 0 {
		b.WriteByte('0')
	} else {
		fmt.Fprintf(b, "%d", 1+g.r.Intn(1<<20))
		if g.r.Intn(5) == 0 {
			b.WriteString("12345678901234567890") // past u64
		}
	}
	if g.r.Intn(3) == 0 {
		fmt.Fprintf(b, ".%d", g.r.Intn(1000))
	}
	if g.r.Intn(4) == 0 {
		b.WriteString([]string{"e", "E", "e+", "e-", "E-"}[g.r.Intn(5)])
		fmt.Fprintf(b, "%d", g.r.Intn(400))
	}
}

func (g *jsonGen) str(b *strings.Builder) {
	b.WriteByte('"')
	for n := g.r.Intn(6); n > 0; n-- {
		switch c := g.r.Intn(12); {
		case c < 5:
			// printable ASCII but `"` and `\`, which only escapes may write
			c := byte(0x20 + g.r.Intn(0x5F))
			if c == '"' || c == '\\' {
				c = 'q'
			}
			b.WriteByte(c)
		case c < 7:
			b.WriteString([]string{`\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`}[g.r.Intn(8)])
		case c < 8:
			fmt.Fprintf(b, `\u%04x`, g.r.Intn(0xD800))
		case c < 9:
			hi, lo := 0xD800+g.r.Intn(0x400), 0xDC00+g.r.Intn(0x400)
			switch g.r.Intn(4) {
			case 0:
				fmt.Fprintf(b, `\u%04X`, hi) // lone high
			case 1:
				fmt.Fprintf(b, `\u%04x`, lo) // lone low
			default:
				fmt.Fprintf(b, `\u%04x\u%04X`, hi, lo)
			}
		default:
			b.WriteRune([]rune{'é', 'ß', '中', '😀', 0x10FFFF, 0x7FF, 0x800}[g.r.Intn(7)])
		}
	}
	b.WriteByte('"')
}

func (g *jsonGen) value(b *strings.Builder, depth int) {
	k := g.r.Intn(8)
	if depth > 3 {
		k = g.r.Intn(4)
	}
	switch k {
	case 0:
		b.WriteString([]string{"null", "true", "false"}[g.r.Intn(3)])
	case 1:
		g.number(b)
	case 2, 3:
		g.str(b)
	case 4, 5:
		b.WriteByte('[')
		for i, n := 0, g.r.Intn(4); i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			g.ws(b)
			g.value(b, depth+1)
			g.ws(b)
		}
		b.WriteByte(']')
	default:
		b.WriteByte('{')
		for i, n := 0, g.r.Intn(4); i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			g.ws(b)
			g.str(b)
			g.ws(b)
			b.WriteByte(':')
			g.ws(b)
			g.value(b, depth+1)
		}
		b.WriteByte('}')
	}
}

// loneSurrogate reports a \u escape of a surrogate that is not half of a
// high-low pair (a mutation can make or break a pair, so the text, not the
// generator, decides). Outside strings a backslash makes the text invalid
// anyway, so every backslash pair is read as an escape.
func loneSurrogate(b []byte) bool {
	hex := func(i int) (int, bool) {
		if i+6 > len(b) || b[i] != '\\' || b[i+1] != 'u' {
			return 0, false
		}
		v := 0
		for _, c := range b[i+2 : i+6] {
			switch {
			case c >= '0' && c <= '9':
				v = v*16 + int(c-'0')
			case c >= 'a' && c <= 'f':
				v = v*16 + int(c-'a'+10)
			case c >= 'A' && c <= 'F':
				v = v*16 + int(c-'A'+10)
			default:
				return 0, false
			}
		}
		return v, true
	}
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		v, ok := hex(i)
		if !ok {
			i++ // a two-byte escape
			continue
		}
		switch {
		case v >= 0xD800 && v <= 0xDBFF:
			lo, ok := hex(i + 6)
			if !ok || lo < 0xDC00 || lo > 0xDFFF {
				return true
			}
			i += 11
		case v >= 0xDC00 && v <= 0xDFFF:
			return true
		default:
			i += 5
		}
	}
	return false
}

// TestJSONDifferential checks std/json against Go's encoding/json
// (specs/std-files R5): 700 texts from a fixed seed, most generated from
// the grammar and a third of those mutated by one byte, parsed by one kern
// program in the interpreter. A text is valid for std/json exactly when Go
// finds it valid, it is UTF-8 and it has no lone surrogate escape; a valid
// text written back by std/json decodes in Go to what the original
// decodes to, numbers compared by their text.
func TestJSONDifferential(t *testing.T) {
	g := &jsonGen{r: rand.New(rand.NewSource(11))}
	type tc struct {
		text []byte
	}
	var cases []tc
	mutations := []byte(",]}\":\\0-.e+x\x01\xc3 [{")
	for len(cases) < 700 {
		var b strings.Builder
		g.ws(&b)
		g.value(&b, 0)
		g.ws(&b)
		text := []byte(b.String())
		if g.r.Intn(3) == 0 && len(text) > 0 {
			at := g.r.Intn(len(text))
			// one byte deleted, inserted or replaced, or one of the
			// near-valid forms a parser is likeliest to accept: a
			// trailing comma, a leading zero, a cut \u escape
			switch g.r.Intn(6) {
			case 3:
				if i := bytes.LastIndexAny(text, "]}"); i >= 0 {
					text = append(append(text[:i:i], ','), text[i:]...)
				}
			case 4:
				if i := bytes.IndexAny(text, "123456789"); i >= 0 {
					text = append(append(text[:i:i], '0'), text[i:]...)
				}
			case 5:
				if i := bytes.Index(text, []byte(`\u`)); i >= 0 && i+4 <= len(text) {
					text = append(text[: i+4 : i+4], text[min(i+6, len(text)):]...)
				}
			case 0:
				text = append(text[:at:at], text[at+1:]...)
			case 1:
				text = append(append(text[:at:at], mutations[g.r.Intn(len(mutations))]), text[at:]...)
			default:
				text[at] = mutations[g.r.Intn(len(mutations))]
			}
		}
		cases = append(cases, tc{text})
	}
	deep := strings.Repeat("[", 600) + strings.Repeat("]", 600)
	cases = append(cases, tc{[]byte(deep)}, tc{[]byte(strings.Repeat("[", 500) + strings.Repeat("]", 500))})
	var src strings.Builder
	src.WriteString("module t\nimport \"std/json\"\nimport \"std/str\"\nfunc report(s: view[]u8) {\n    with r := json.parse(s) {\n        match r {\n            Ok(v) => {\n                d := str.String.new()\n                json.write(v, d)\n                print_str(\"ok \")\n                print_str(d.as_view())\n            }\n            Err(e) => {\n                match e {\n                    Syntax(at) => print_str(\"syntax\")\n                    TooDeep(at) => print_str(\"deep\")\n                }\n            }\n        }\n    }\n    println()\n}\nfunc main() -> i32 {\n    s := str.String.new()\n")
	for _, c := range cases {
		src.WriteString("    s.clear()\n")
		for _, x := range c.text {
			fmt.Fprintf(&src, "    s.push(%du8)\n", x)
		}
		src.WriteString("    report(s.as_view())\n")
	}
	src.WriteString("    return 0i32\n}\n")
	files := []*source.File{{Name: "json.kern", Bytes: []byte(src.String())}}
	prog := resolve.Resolve(files)
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %s", ds[0].Message)
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if err := opt.Run(mod, opt.Options{}); err != nil {
		t.Fatalf("opt: %v", err)
	}
	var out, errb bytes.Buffer
	if status := interp.New(mod, "json.kern", &out, &errb).Run(); status != 0 {
		t.Fatalf("interpreter status %d: %s", status, errb.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("%d output lines, want %d", len(lines), len(cases))
	}
	decode := func(b []byte) (any, error) {
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		var v any
		if err := d.Decode(&v); err != nil {
			return nil, err
		}
		// one value and nothing after it
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("data after the value: %v", err)
		}
		return v, nil
	}
	valid, invalid := 0, 0
	for i, c := range cases {
		got := lines[i]
		if i == len(cases)-2 {
			if got != "deep" {
				t.Errorf("600 nested arrays: %q, want deep", got)
			}
			continue
		}
		want := json.Valid(c.text) && utf8.Valid(c.text) && !loneSurrogate(c.text)
		if want {
			valid++
		} else {
			invalid++
		}
		if !want {
			if got != "syntax" {
				t.Errorf("case %d %q: kern %q, want syntax", i, c.text, got)
			}
			continue
		}
		written, ok := strings.CutPrefix(got, "ok ")
		if !ok {
			t.Errorf("case %d %q: kern %q, want ok", i, c.text, got)
			continue
		}
		a, err1 := decode(c.text)
		w, err2 := decode([]byte(written))
		if err1 != nil || err2 != nil || !reflect.DeepEqual(a, w) {
			t.Errorf("case %d %q: written %q decodes differently (%v %v)", i, c.text, written, err1, err2)
		}
	}
	if valid < 200 || invalid < 100 {
		t.Fatalf("%d valid and %d invalid texts; want at least 200 and 100", valid, invalid)
	}
}
