package tests

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// TestIntegerSemanticsDifferential is the integer counterpart of
// TestProvenChecksDifferential: random expressions over every integer type
// with the trapping operators, the wrapping, saturating and checked forms,
// conversions, truncation, negation and shifts (spec §4.1), with operands
// drawn near the type limits, printed by a program the interpreter and both
// native builds must agree on: output, exit status and the trap record. The
// two engines implement the operations separately (Go arithmetic against
// LLVM instructions and intrinsics), so a disagreement is a bug in one of
// them. KERN_DIFF_PROGRAMS, KERN_DIFF_SEED and KERN_DIFF_DUMP apply.
func TestIntegerSemanticsDifferential(t *testing.T) {
	if !native.Available() {
		t.Skip("clang not available")
	}
	n := 40
	if s := os.Getenv("KERN_DIFF_PROGRAMS"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			t.Fatalf("KERN_DIFF_PROGRAMS=%q", s)
		}
		n = v
	}
	seed := int64(1)
	if s := os.Getenv("KERN_DIFF_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("KERN_DIFF_SEED=%q", s)
		}
		seed = v
	}
	for i := range n {
		s := seed + int64(i)
		t.Run(fmt.Sprintf("seed%d", s), func(t *testing.T) {
			t.Parallel()
			src := genArith(rand.New(rand.NewSource(s)))
			if os.Getenv("KERN_DIFF_DUMP") != "" {
				t.Log(src)
			}
			f := &source.File{Name: "arith.kern", Bytes: []byte(src)}
			files := []*source.File{f}
			prog := resolve.Resolve(files)
			if len(prog.Diags) > 0 {
				t.Fatalf("generated program does not resolve: %s\n%s", prog.Diags[0].Message, src)
			}
			if ds := check.Check(prog); len(ds) > 0 {
				t.Fatalf("generated program does not check: %s\n%s", ds[0].Message, src)
			}
			mod, ds := lower.Lower(prog)
			if len(ds) > 0 {
				t.Fatalf("lower: %s\n%s", ds[0].Message, src)
			}
			if err := opt.Run(mod, opt.Options{}); err != nil {
				t.Fatalf("validate: %v\n%s", err, src)
			}
			var out, errb bytes.Buffer
			it := interp.New(mod, "arith.kern", &out, &errb)
			status := it.Run()
			if status != 0 && status != 134 {
				t.Fatalf("interpreter status %d, stderr %q\n%s", status, errb.String(), src)
			}
			for _, level := range []string{"debug", "release"} {
				t.Run(level, func(t *testing.T) {
					defer func() {
						if t.Failed() {
							t.Logf("program:\n%s", src)
						}
					}()
					nativeRunOpt(t, files, header{}, out.Bytes(), errb.Bytes(), status, level)
				})
			}
		})
	}
}

var intTypes = []string{"u8", "u16", "u32", "u64", "i8", "i16", "i32", "i64"}

// lit writes a literal of type ty, with values near the type's limits more
// often than not.
func lit(r *rand.Rand, ty string) string {
	bits := map[string]uint{"u8": 8, "u16": 16, "u32": 32, "u64": 64, "i8": 8, "i16": 16, "i32": 32, "i64": 64}[ty]
	signed := ty[0] == 'i'
	var v uint64
	switch r.Intn(6) {
	case 0:
		v = 0
	case 1:
		v = 1
	case 2: // the unsigned maximum, or the signed one
		if signed {
			v = 1<<(bits-1) - 1
		} else {
			v = 1<<bits - 1
			if bits == 64 {
				v = ^uint64(0)
			}
		}
	case 3:
		v = uint64(r.Intn(100))
	default: // anywhere in range
		if bits == 64 {
			v = r.Uint64()
		} else {
			v = r.Uint64() & (1<<bits - 1)
		}
		if signed {
			v &= 1<<(bits-1) - 1
		}
	}
	if signed && r.Intn(3) == 0 {
		// the negative side, including the minimum
		if r.Intn(4) == 0 {
			return fmt.Sprintf("-%d%s", uint64(1)<<(bits-1), ty)
		}
		return fmt.Sprintf("-%d%s", v, ty)
	}
	return fmt.Sprintf("%d%s", v, ty)
}

// expr writes an expression of type ty up to depth deep. trapping says
// whether the trapping operators may appear (they end the program at the
// first trap, so most statements avoid them).
func expr(r *rand.Rand, ty string, depth int, trapping bool) string {
	if depth == 0 || r.Intn(4) == 0 {
		return lit(r, ty)
	}
	sub := func() string { return expr(r, ty, depth-1, trapping) }
	cnt := fmt.Sprintf("%du32", r.Intn(70)) // shift counts past the width too
	switch r.Intn(trappingChoices(trapping)) {
	case 0:
		return fmt.Sprintf("wrapping_add(%s, %s)", sub(), sub())
	case 1:
		return fmt.Sprintf("wrapping_sub(%s, %s)", sub(), sub())
	case 2:
		return fmt.Sprintf("wrapping_mul(%s, %s)", sub(), sub())
	case 3:
		return fmt.Sprintf("wrapping_neg(%s)", sub())
	case 4:
		return fmt.Sprintf("wrapping_shl(%s, %s)", sub(), cnt)
	case 5:
		return fmt.Sprintf("wrapping_shr(%s, %s)", sub(), cnt)
	case 6:
		return fmt.Sprintf("saturating_add(%s, %s)", sub(), sub())
	case 7:
		return fmt.Sprintf("saturating_sub(%s, %s)", sub(), sub())
	case 8:
		return fmt.Sprintf("saturating_mul(%s, %s)", sub(), sub())
	case 9:
		return fmt.Sprintf("(%s & %s)", sub(), sub())
	case 10:
		return fmt.Sprintf("(%s | %s)", sub(), sub())
	case 11:
		return fmt.Sprintf("(%s ^ %s)", sub(), sub())
	case 12:
		// a truncation from another type never traps
		from := intTypes[r.Intn(len(intTypes))]
		return fmt.Sprintf("truncate[%s](%s)", ty, expr(r, from, depth-1, trapping))
	case 13:
		// a checked form unwrapped with a default
		return fmt.Sprintf("unwrap_%s(%s, %s)", ty, checkedCall(r, sub, cnt), lit(r, ty))
	case 14:
		return fmt.Sprintf("(%s + %s)", sub(), sub())
	case 15:
		return fmt.Sprintf("(%s - %s)", sub(), sub())
	case 16:
		return fmt.Sprintf("(%s * %s)", sub(), sub())
	case 17:
		return fmt.Sprintf("(%s / %s)", sub(), sub())
	case 18:
		return fmt.Sprintf("(%s %% %s)", sub(), sub())
	case 19:
		return fmt.Sprintf("(%s << %s)", sub(), cnt)
	case 20:
		return fmt.Sprintf("(%s >> %s)", sub(), cnt)
	case 21:
		if ty[0] == 'i' {
			return fmt.Sprintf("(-%s)", sub())
		}
		// a negated unsigned literal is a compile-time error, not a trap:
		// negate a non-literal (any value but zero traps)
		return fmt.Sprintf("(-wrapping_add(%s, 0%s))", sub(), ty)
	default:
		// a trapping conversion from another type
		from := intTypes[r.Intn(len(intTypes))]
		return fmt.Sprintf("%s(%s)", ty, expr(r, from, depth-1, trapping))
	}
}

func trappingChoices(trapping bool) int {
	if trapping {
		return 23
	}
	return 14
}

func checkedCall(r *rand.Rand, sub func() string, cnt string) string {
	switch r.Intn(7) {
	case 0:
		return fmt.Sprintf("checked_add(%s, %s)", sub(), sub())
	case 1:
		return fmt.Sprintf("checked_sub(%s, %s)", sub(), sub())
	case 2:
		return fmt.Sprintf("checked_mul(%s, %s)", sub(), sub())
	case 3:
		return fmt.Sprintf("checked_div(%s, %s)", sub(), sub())
	case 4:
		return fmt.Sprintf("checked_rem(%s, %s)", sub(), sub())
	case 5:
		return fmt.Sprintf("checked_shl(%s, %s)", sub(), cnt)
	default:
		return fmt.Sprintf("checked_shr(%s, %s)", sub(), cnt)
	}
}

// genArith writes one program: an unwrap helper per type, then statements
// printing expressions; about one statement in six uses the trapping
// operators, so a program usually prints several lines before its trap.
func genArith(r *rand.Rand) string {
	var b strings.Builder
	b.WriteString("module a\n")
	for _, ty := range intTypes {
		fmt.Fprintf(&b, "func unwrap_%s(o: Option[%s], d: %s) -> %s {\n    match o {\n        Some(v) => return v\n        None => return d\n    }\n}\n", ty, ty, ty, ty)
	}
	b.WriteString("func main() -> i32 {\n")
	stmts := 4 + r.Intn(10)
	for range stmts {
		ty := intTypes[r.Intn(len(intTypes))]
		e := expr(r, ty, 1+r.Intn(3), r.Intn(6) == 0)
		if ty[0] == 'i' {
			fmt.Fprintf(&b, "    print_i64(i64(%s))\n    println()\n", e)
		} else {
			fmt.Fprintf(&b, "    print_u64(u64(%s))\n    println()\n", e)
		}
	}
	b.WriteString("    return 0i32\n}\n")
	return b.String()
}
