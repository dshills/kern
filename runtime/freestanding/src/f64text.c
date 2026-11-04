// kern freestanding floating-point text (specs/freestanding-float): the two
// std/str runtime entries the hosted runtime implements with snprintf and
// strtod, computed exactly with fixed-size big integers on the stack. No
// allocation, no libc, no trap; every helper is static.
#include <stdint.h>
#include <stddef.h>

// f64t_big is an unsigned integer of n 32-bit limbs, least significant
// first, with w[n-1] != 0 (n == 0 is zero). 160 limbs (5120 bits) hold
// every intermediate value (specs/freestanding-float §2: below 2^4300).
#define F64T_LIMBS 160
typedef struct { int n; uint32_t w[F64T_LIMBS]; } f64t_big;

static void f64t_set(f64t_big *a, uint64_t v) {
    a->n = 0;
    while (v) { a->w[a->n++] = (uint32_t)v; v >>= 32; }
}

static void f64t_copy(f64t_big *d, const f64t_big *s) {
    d->n = s->n;
    for (int i = 0; i < s->n; i++) d->w[i] = s->w[i];
}

// a = a*m + add
static void f64t_muladd(f64t_big *a, uint32_t m, uint32_t add) {
    uint64_t c = add;
    for (int i = 0; i < a->n; i++) {
        c += (uint64_t)a->w[i] * m;
        a->w[i] = (uint32_t)c;
        c >>= 32;
    }
    if (c) a->w[a->n++] = (uint32_t)c;
}

// a = a * 10^k
static void f64t_mulpow10(f64t_big *a, int64_t k) {
    static const uint32_t p10[10] = {1u, 10u, 100u, 1000u, 10000u, 100000u, 1000000u, 10000000u, 100000000u, 1000000000u};
    for (; k >= 9; k -= 9) f64t_muladd(a, p10[9], 0);
    if (k > 0) f64t_muladd(a, p10[k], 0);
}

// a = a << s
static void f64t_shl(f64t_big *a, int64_t s) {
    if (a->n == 0 || s <= 0) return;
    int q = (int)(s / 32), r = (int)(s % 32);
    if (r) {
        uint32_t c = 0;
        for (int i = 0; i < a->n; i++) {
            uint32_t x = a->w[i];
            a->w[i] = (x << r) | c;
            c = x >> (32 - r);
        }
        if (c) a->w[a->n++] = c;
    }
    if (q) {
        for (int i = a->n - 1; i >= 0; i--) a->w[i + q] = a->w[i];
        for (int i = 0; i < q; i++) a->w[i] = 0;
        a->n += q;
    }
}

// a = a >> 1
static void f64t_shr1(f64t_big *a) {
    for (int i = 0; i < a->n; i++) {
        uint32_t hi = i + 1 < a->n ? a->w[i + 1] : 0;
        a->w[i] = (a->w[i] >> 1) | (hi << 31);
    }
    if (a->n > 0 && a->w[a->n - 1] == 0) a->n--;
}

static int f64t_cmp(const f64t_big *a, const f64t_big *b) {
    if (a->n != b->n) return a->n < b->n ? -1 : 1;
    for (int i = a->n - 1; i >= 0; i--) {
        if (a->w[i] != b->w[i]) return a->w[i] < b->w[i] ? -1 : 1;
    }
    return 0;
}

// a = a - b, for a >= b
static void f64t_sub(f64t_big *a, const f64t_big *b) {
    int64_t borrow = 0;
    for (int i = 0; i < a->n; i++) {
        int64_t d = (int64_t)a->w[i] - (i < b->n ? (int64_t)b->w[i] : 0) - borrow;
        borrow = d < 0;
        a->w[i] = (uint32_t)(d + (borrow ? ((int64_t)1 << 32) : 0));
    }
    while (a->n > 0 && a->w[a->n - 1] == 0) a->n--;
}

static int64_t f64t_bitlen(const f64t_big *a) {
    if (a->n == 0) return 0;
    uint32_t top = a->w[a->n - 1];
    int64_t b = 0;
    while (top) { b++; top >>= 1; }
    return (int64_t)(a->n - 1) * 32 + b;
}

// compares 2a with b
static int f64t_cmp_twice(const f64t_big *a, const f64t_big *b) {
    f64t_big t;
    f64t_copy(&t, a);
    f64t_shl(&t, 1);
    return f64t_cmp(&t, b);
}

static uint64_t f64t_bits(double v) {
    union { double d; uint64_t u; } x;
    x.d = v;
    return x.u;
}

static double f64t_from_bits(uint64_t u) {
    union { double d; uint64_t u; } x;
    x.u = u;
    return x.d;
}

// f64t_digits18 writes the first 18 correctly truncated significant
// decimal digits of a finite nonzero |v| to dig (as values 0..9), sets
// *sticky when the remainder beyond them is nonzero, and returns the
// decimal exponent E with 10^E <= |v| < 10^(E+1).
static int64_t f64t_digits18(uint64_t bits, uint8_t dig[18], int *sticky) {
    int64_t ex = (int64_t)((bits >> 52) & 0x7ff);
    uint64_t m = bits & ((1ull << 52) - 1);
    int64_t e;
    if (ex == 0) {
        e = -1074;
    } else {
        m |= 1ull << 52;
        e = ex - 1075;
    }
    f64t_big num, den, t;
    f64t_set(&num, m);
    f64t_set(&den, 1);
    if (e >= 0) f64t_shl(&num, e); else f64t_shl(&den, -e);
    // floor(log10 |v|) is floor(exp2 * log10 2) or one more; the estimate
    // is corrected below in both directions
    int64_t exp2 = f64t_bitlen(&num) - f64t_bitlen(&den);
    int64_t prod = exp2 * 78913;
    int64_t E = prod >= 0 ? prod >> 18 : -((-prod + (1 << 18) - 1) >> 18);
    if (E >= 0) f64t_mulpow10(&den, E); else f64t_mulpow10(&num, -E);
    while (f64t_cmp(&num, &den) < 0) {
        f64t_muladd(&num, 10, 0);
        E--;
    }
    for (;;) {
        f64t_copy(&t, &den);
        f64t_muladd(&t, 10, 0);
        if (f64t_cmp(&num, &t) < 0) break;
        f64t_copy(&den, &t);
        E++;
    }
    for (int i = 0; i < 18; i++) {
        uint8_t d = 0;
        while (f64t_cmp(&num, &den) >= 0) {
            f64t_sub(&num, &den);
            d++;
        }
        dig[i] = d;
        if (i < 17) f64t_muladd(&num, 10, 0);
    }
    *sticky = num.n != 0;
    return E;
}

// f64t_format writes v correctly rounded (ties to even) to p significant
// digits as snprintf's "%.*e" with precision p-1 does: [-]d[.ddd]e(+|-)XX.
// dig, E and sticky are f64t_digits18's for v (unused for zero). Returns
// the length; out has room for 32 bytes.
static int f64t_format(uint64_t bits, const uint8_t dig[18], int64_t E, int sticky, int p, char *out) {
    int n = 0;
    if (bits >> 63) out[n++] = '-';
    char d[18];
    if ((bits << 1) == 0) {
        for (int i = 0; i < p; i++) d[i] = '0';
        E = 0;
    } else {
        for (int i = 0; i < p; i++) d[i] = (char)('0' + dig[i]);
        // the rest beyond p digits against one half of the last place
        int up;
        if (dig[p] != 5) {
            up = dig[p] > 5;
        } else {
            int more = sticky;
            for (int i = p + 1; i < 18; i++) more |= dig[i] != 0;
            up = more || ((dig[p - 1] & 1) != 0);
        }
        if (up) {
            int i = p - 1;
            while (i >= 0 && d[i] == '9') d[i--] = '0';
            if (i < 0) {
                d[0] = '1';
                E++;
            } else {
                d[i]++;
            }
        }
    }
    out[n++] = d[0];
    if (p > 1) {
        out[n++] = '.';
        for (int i = 1; i < p; i++) out[n++] = d[i];
    }
    out[n++] = 'e';
    uint64_t x;
    if (E < 0) {
        out[n++] = '-';
        x = (uint64_t)(-E);
    } else {
        out[n++] = '+';
        x = (uint64_t)E;
    }
    char r[8];
    int k = 0;
    do { r[k++] = (char)('0' + x % 10); x /= 10; } while (x);
    if (k < 2) r[k++] = '0';
    while (k > 0) out[n++] = r[--k];
    return n;
}

// f64t_parse is the correctly rounded value (ties to even) of p[0..n),
// which is one JSON number: ±Inf when it overflows, ±0 when it underflows.
static double f64t_parse(const char *p, int64_t n) {
    enum { KEEP = 800 };
    uint8_t dig[KEEP + 1];
    int nd = 0, sticky = 0;
    int64_t i = 0, eadj = 0;
    uint64_t sign = 0;
    if (i < n && p[i] == '-') {
        sign = 1ull << 63;
        i++;
    }
    for (; i < n && p[i] >= '0' && p[i] <= '9'; i++) {
        uint8_t c = (uint8_t)(p[i] - '0');
        if (nd == 0 && c == 0) continue;
        if (nd < KEEP) {
            dig[nd++] = c;
        } else {
            sticky |= c != 0;
            eadj++;
        }
    }
    if (i < n && p[i] == '.') {
        for (i++; i < n && p[i] >= '0' && p[i] <= '9'; i++) {
            uint8_t c = (uint8_t)(p[i] - '0');
            if (nd == 0 && c == 0) {
                eadj--;
            } else if (nd < KEEP) {
                dig[nd++] = c;
                eadj--;
            } else {
                sticky |= c != 0;
            }
        }
    }
    int64_t ex = 0;
    if (i < n && (p[i] == 'e' || p[i] == 'E')) {
        i++;
        int eneg = 0;
        if (i < n && (p[i] == '+' || p[i] == '-')) {
            eneg = p[i] == '-';
            i++;
        }
        // saturates below 10^18: the digit counts that adjust it are below the
        // text's length, so for any text that fits in memory a saturated
        // exponent still leaves the value far out of range
        for (; i < n && p[i] >= '0' && p[i] <= '9'; i++) {
            if (ex < 100000000000000000) ex = ex * 10 + (p[i] - '0');
        }
        if (eneg) ex = -ex;
    }
    if (nd == 0) return f64t_from_bits(sign);
    if (sticky) {
        dig[nd++] = 1;
        eadj--;
    }
    int64_t E = eadj + ex;
    int64_t lead = (int64_t)nd - 1 + E;
    if (lead > 309) return f64t_from_bits(sign | 0x7ff0000000000000ull);
    if (lead < -324) return f64t_from_bits(sign);
    f64t_big num, den, d;
    f64t_set(&num, 0);
    f64t_set(&den, 1);
    for (int j = 0; j < nd; j++) f64t_muladd(&num, 10, dig[j]);
    if (E >= 0) f64t_mulpow10(&num, E); else f64t_mulpow10(&den, -E);
    // k = floor(log2(num/den)): the bit-length difference or one less
    int64_t k = f64t_bitlen(&num) - f64t_bitlen(&den);
    if (k >= 0) {
        f64t_copy(&d, &den);
        f64t_shl(&d, k);
        if (f64t_cmp(&num, &d) < 0) k--;
    } else {
        f64t_copy(&d, &num);
        f64t_shl(&d, -k);
        if (f64t_cmp(&d, &den) < 0) k--;
    }
    if (k > 1023) return f64t_from_bits(sign | 0x7ff0000000000000ull);
    int sub = k < -1022;
    int64_t s = sub ? 1074 : 52 - k;
    if (s >= 0) f64t_shl(&num, s); else f64t_shl(&den, -s);
    // q = floor(num/den) < 2^53, by shift-and-subtract; num keeps the
    // remainder
    uint64_t q = 0;
    f64t_copy(&d, &den);
    f64t_shl(&d, 52);
    for (int b = 52; b >= 0; b--) {
        if (b < 52) f64t_shr1(&d);
        if (f64t_cmp(&num, &d) >= 0) {
            f64t_sub(&num, &d);
            q |= 1ull << b;
        }
    }
    int c = f64t_cmp_twice(&num, &den);
    if (c > 0 || (c == 0 && (q & 1))) q++;
    if (sub) return f64t_from_bits(sign | q); // 2^52 after rounding is the smallest normal
    if (q == 1ull << 53) {
        q >>= 1;
        k++;
    }
    if (k > 1023) return f64t_from_bits(sign | 0x7ff0000000000000ull);
    return f64t_from_bits(sign | ((uint64_t)(k + 1023) << 52) | (q & ((1ull << 52) - 1)));
}

// kern_str_f64_digits writes the round-trip digits of a finite v in
// exponent form ([-]d[.ddd]e+XX): the fewest correctly rounded
// significant digits, 1 to 17, that read back as v. Returns the length.
int64_t kern_str_f64_digits(double v, char *buf, int64_t n) {
    uint64_t bits = f64t_bits(v);
    uint8_t dig[18] = {0};
    int sticky = 0;
    int64_t E = 0;
    if ((bits << 1) != 0) E = f64t_digits18(bits, dig, &sticky);
    char tmp[32];
    int len = 0;
    for (int p = 1; p <= 17; p++) {
        len = f64t_format(bits, dig, E, sticky, p, tmp);
        if (f64t_bits(f64t_parse(tmp, len)) == bits) break;
    }
    if (n < 0) n = 0;
    if (len > n) len = (int)n;
    for (int i = 0; i < len; i++) buf[i] = tmp[i];
    return len;
}

// kern_str_parse_f64 is the correctly rounded value of text already
// checked to be one JSON number (±Inf when it overflows). It needs no
// copy and no allocation, so it never traps; at is unused.
double kern_str_parse_f64(const char *p, int64_t n, const void *at) {
    (void)at;
    return f64t_parse(p, n);
}
