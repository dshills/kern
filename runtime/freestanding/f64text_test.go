package freestanding

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var f64Scale = flag.Int("f64text-scale", 1, "multiply the random case counts of the float-text differential test")

// f64Driver compiles f64text.c with testdata/f64driver.c for the host.
func f64Driver(t *testing.T) string {
	t.Helper()
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "f64text.c")
	if err := os.WriteFile(src, []byte(F64TextC), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "f64driver")
	if out, err := exec.Command(clang, "-O2", "-o", exe, filepath.Join("testdata", "f64driver.c"), src).CombinedOutput(); err != nil {
		t.Fatalf("clang: %v\n%s", err, out)
	}
	return exe
}

// runDriver sends the request lines and returns one answer per line.
func runDriver(t *testing.T, exe string, reqs []string) []string {
	t.Helper()
	cmd := exec.Command(exe)
	cmd.Stdin = strings.NewReader(strings.Join(reqs, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("driver: %v", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != len(reqs) {
		t.Fatalf("driver answered %d of %d requests", len(lines), len(reqs))
	}
	return lines
}

// goDigits is the round-trip digits by strconv, the interpreter's
// reference (specs/float-text).
func goDigits(v float64) string {
	var s string
	for p := 1; p <= 17; p++ {
		s = strconv.FormatFloat(v, 'e', p-1, 64)
		if r, _ := strconv.ParseFloat(s, 64); math.Float64bits(r) == math.Float64bits(v) {
			break
		}
	}
	return s
}

func goParse(s string) uint64 {
	v, _ := strconv.ParseFloat(s, 64) // ±Inf on a range error, ±0 on underflow
	return math.Float64bits(v)
}

// specialValues are the values of specs/freestanding-float §4.
func specialValues() []float64 {
	vs := []float64{0, math.Copysign(0, -1), math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64,
		math.Float64frombits(0x000fffffffffffff), math.Float64frombits(0x0010000000000000), math.MaxFloat64, -math.MaxFloat64,
		1, 0.1, 0.3, math.Ldexp(1, -24), 5e-324, 1e23, 9007199254740993,
		// a carry out of all nines at small p: 9.5 is 1e+01 at p = 1
		9.5, 99.5, 0.095, 999.75, 9.999999999999999e22}
	for e := -323; e <= 308; e++ {
		v, _ := strconv.ParseFloat("1e"+strconv.Itoa(e), 64)
		vs = append(vs, v)
	}
	for e := -1074; e <= 1023; e++ {
		vs = append(vs, math.Ldexp(1, e))
	}
	return vs
}

// longTexts are texts strconv.ParseFloat reads wrong (it stops reading
// an exponent near 10000 before adding the digit position) with their
// correctly rounded values; the interpreter rewrites them first
// (compiler/interp canonicalNumber, R9).
var longTexts = map[string]float64{
	"0." + strings.Repeat("0", 100001) + "1e100002":   1,
	strings.Repeat("1", 100001) + "e-100001":          1.0 / 9,
	"1" + strings.Repeat("0", 100001) + "e-100001":    1,
	"-0." + strings.Repeat("0", 100001) + "25e100002": -2.5,
	// strconv's slow path reads this 801-digit integer 10 times too small
	"100000000000000011102230246251565404236316680908203125" + strings.Repeat("0", 746) + "1e-800": math.Nextafter(1, 2),
}

func specialTexts() []string {
	ts := []string{"0", "-0", "0.0e0", "1e400", "-1e400", "1e-400", "2.4703282292062327e-324", "2.4703282292062328e-324",
		"1.7976931348623158e308", "1.7976931348623159e308", "1e99999999999", "1e-99999999999", "-0.0e-99999999999",
		"9007199254740993", "9007199254740993.0000000000000000000001",
		"9007199254740992.9999999999999999999999"}
	// a halfway point, then many zeros, then a final nonzero digit
	h := halfway(1, math.Nextafter(1, 2))
	ts = append(ts, h+strings.Repeat("0", 2000)+"1", h+strings.Repeat("0", 2000))
	for s := range longTexts {
		ts = append(ts, s)
	}
	return ts
}

// halfway is the exact decimal expansion of the midpoint of a and b,
// adjacent finite doubles, in exponent form.
func halfway(a, b float64) string {
	x := new(big.Float).SetPrec(2100).SetFloat64(a)
	y := new(big.Float).SetPrec(2100).SetFloat64(b)
	x.Add(x, y).Quo(x, big.NewFloat(2))
	s := x.Text('e', 800) // exact: a midpoint has at most 767 significant digits
	mant, exp, _ := strings.Cut(s, "e")
	mant = strings.TrimRight(mant, "0")
	mant = strings.TrimSuffix(mant, ".")
	return mant + "e" + exp
}

func randomFinite(r *rand.Rand) float64 {
	for {
		v := math.Float64frombits(r.Uint64())
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v
		}
	}
}

func randomJSONNumber(r *rand.Rand) string {
	var b strings.Builder
	if r.Intn(2) == 0 {
		b.WriteByte('-')
	}
	if n := 1 + r.Intn(30); r.Intn(10) == 0 {
		b.WriteByte('0')
	} else {
		b.WriteByte(byte('1' + r.Intn(9)))
		for i := 1; i < n; i++ {
			b.WriteByte(byte('0' + r.Intn(10)))
		}
	}
	if r.Intn(2) == 0 {
		b.WriteByte('.')
		for i, n := 0, 1+r.Intn(20); i < n; i++ {
			b.WriteByte(byte('0' + r.Intn(10)))
		}
	}
	if r.Intn(3) != 0 {
		b.WriteByte("eE"[r.Intn(2)])
		switch r.Intn(3) {
		case 0:
			b.WriteByte('-')
		case 1:
			b.WriteByte('+')
		}
		b.WriteString(strconv.Itoa(r.Intn(401)))
	}
	return b.String()
}

// nudged are the exact midpoint text of v and the next double up, and the
// texts just above and just below it.
func nudged(v float64) []string {
	w := math.Nextafter(v, math.Inf(1))
	if math.IsInf(w, 0) {
		return nil
	}
	h := halfway(v, w)
	mant, exp, _ := strings.Cut(h, "e")
	if !strings.Contains(mant, ".") {
		mant += "."
	}
	above := mant + "000001e" + exp
	// below: lower the last nonzero digit and append nines
	m := []byte(mant)
	i := len(m) - 1
	for i >= 0 && (m[i] == '0' || m[i] == '.') {
		i--
	}
	if i < 0 {
		return []string{h, above}
	}
	m[i]--
	below := string(m) + "9999e" + exp
	return []string{h, above, below}
}

// checkDigits checks R1 for each value three ways.
func checkDigits(t *testing.T, exe string, vs []float64) {
	t.Helper()
	reqs := make([]string, len(vs))
	for i, v := range vs {
		reqs[i] = fmt.Sprintf("D %x", math.Float64bits(v))
	}
	bad := 0
	for i, line := range runDriver(t, exe, reqs) {
		fs, libc, _ := strings.Cut(line, " ")
		if want := goDigits(vs[i]); fs != libc || fs != want {
			t.Errorf("digits of %v (%#016x): freestanding %q, libc %q, strconv %q", vs[i], math.Float64bits(vs[i]), fs, libc, want)
			if bad++; bad > 20 {
				t.FailNow()
			}
		}
	}
}

// checkParse checks R2 for each text three ways.
func checkParse(t *testing.T, exe string, texts []string) {
	t.Helper()
	reqs := make([]string, len(texts))
	for i, s := range texts {
		reqs[i] = "P " + s
	}
	bad := 0
	for i, line := range runDriver(t, exe, reqs) {
		fs, libc, _ := strings.Cut(line, " ")
		want := fmt.Sprintf("%016x", goParse(texts[i]))
		if v, ok := longTexts[texts[i]]; ok {
			want = fmt.Sprintf("%016x", math.Float64bits(v))
		}
		if fs != libc || fs != want {
			s := texts[i]
			if len(s) > 120 {
				s = s[:60] + "…" + s[len(s)-60:]
			}
			t.Errorf("parse of %q: freestanding %s, libc %s, strconv %s", s, fs, libc, want)
			if bad++; bad > 20 {
				t.FailNow()
			}
		}
	}
}

// The freestanding entries agree with the hosted runtime's libc calls and
// with strconv on the special cases and on random inputs
// (specs/freestanding-float R1, R2, R5).
func TestF64TextDifferential(t *testing.T) {
	exe := f64Driver(t)
	r := rand.New(rand.NewSource(20261104))
	n := 100000 * *f64Scale

	vs := specialValues()
	checkDigits(t, exe, vs)
	var texts []string
	for _, v := range vs {
		for p := 1; p <= 17; p++ {
			texts = append(texts, strconv.FormatFloat(v, 'e', p-1, 64))
		}
	}
	texts = append(texts, specialTexts()...)
	specials := texts
	checkParse(t, exe, texts)
	texts = nil

	random := make([]float64, n)
	for i := range random {
		random[i] = randomFinite(r)
	}
	checkDigits(t, exe, random)
	texts = texts[:0]
	for _, v := range random {
		for p := 1; p <= 17; p++ {
			texts = append(texts, strconv.FormatFloat(v, 'e', p-1, 64))
		}
	}
	checkParse(t, exe, texts)

	formatted := len(texts)
	texts = texts[:0]
	for i := 0; i < n; i++ {
		texts = append(texts, randomJSONNumber(r))
	}
	for i := 0; i < n/10; i++ {
		texts = append(texts, nudged(randomFinite(r))...)
	}
	checkParse(t, exe, texts)
	t.Logf("digits: %d special and %d random values; parses: %d special texts, %d formatted texts, %d random and halfway texts",
		len(vs), n, len(specials), formatted, len(texts))
}

// Output longer than the buffer is cut to it, and a negative length is
// zero, as in the hosted entry (R1).
func TestF64TextTruncation(t *testing.T) {
	exe := f64Driver(t)
	var reqs []string
	for _, v := range []float64{0, -1.5, math.MaxFloat64, -math.SmallestNonzeroFloat64, 0.1} {
		for n := -2; n <= 30; n++ {
			reqs = append(reqs, fmt.Sprintf("T %x %d", math.Float64bits(v), n))
		}
	}
	for i, line := range runDriver(t, exe, reqs) {
		fs, libc, _ := strings.Cut(line, " ")
		if fs != libc {
			t.Errorf("%s: freestanding %q, libc %q", reqs[i], fs, libc)
		}
	}
}

// f64text.c needs nothing outside itself, not even a memory routine,
// defines only the two entries, and keeps each entry's stack
// under 8 KiB for both freestanding targets (R3).
func TestF64TextSelfContained(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not on PATH")
	}
	nm, err := exec.LookPath("llvm-nm")
	if err != nil {
		t.Skip("llvm-nm not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "f64text.c")
	if err := os.WriteFile(src, []byte(F64TextC), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, triple := range []string{"aarch64-unknown-none-elf", "x86_64-unknown-none-elf"} {
		obj := filepath.Join(dir, triple+".o")
		args := []string{"-target", triple, "-ffreestanding", "-nostdlib", "-fno-builtin", "-O1", "-ffp-model=strict", "-fstack-usage", "-c", "-o", obj, src}
		cmd := exec.Command(clang, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clang %s: %v\n%s", triple, err, out)
		}
		out, err := exec.Command(nm, "--undefined-only", "--format=just-symbols", obj).Output()
		if err != nil {
			t.Fatal(err)
		}
		if u := strings.Fields(string(out)); len(u) > 0 {
			t.Errorf("%s: f64text.c needs %v from outside itself", triple, u)
		}
		out, err = exec.Command(nm, "--defined-only", "--extern-only", "--format=just-symbols", obj).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(strings.Fields(string(out)), " "); got != "kern_str_f64_digits kern_str_parse_f64" {
			t.Errorf("%s: f64text.c defines %s", triple, got)
		}
		// the sum of every function's frame bounds each entry's stack
		su, err := os.ReadFile(strings.TrimSuffix(obj, ".o") + ".su")
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, l := range strings.Split(strings.TrimSpace(string(su)), "\n") {
			f := strings.Split(l, "\t")
			if len(f) < 3 || f[2] != "static" {
				t.Errorf("%s: frame not static: %q", triple, l)
				continue
			}
			n, _ := strconv.Atoi(f[1])
			total += n
		}
		if total >= 8192 {
			t.Errorf("%s: f64text.c frames total %d bytes, want under 8 KiB", triple, total)
		}
		t.Logf("%s: frames total %d bytes", triple, total)
	}
}
