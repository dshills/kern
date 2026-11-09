// kern hosted runtime: std/str floating-point text.
// Included by kern_rt.c; see kern_rt.h for the entries.

// ---- std/str floating-point text (specs/float-text) ----
//
// The runtime never calls setlocale or fesetround, so snprintf and strtod
// run in the C locale with round-to-nearest, both correctly rounded; the
// interpreter runs the same search with Go's strconv.

// kern_str_f64_digits writes the round-trip digits of a finite v in
// exponent form ([-]d[.ddd]e+XX): the fewest correctly rounded
// significant digits, 1 to 17, that read back as v. Returns the length.
int64_t kern_str_f64_digits(double v, char *buf, int64_t n) {
    char tmp[40];
    int len = 0;
    for (int p = 1; p <= 17; p++) {
        len = snprintf(tmp, sizeof tmp, "%.*e", p - 1, v);
        if (strtod(tmp, NULL) == v) break;
    }
    if (n < 0) n = 0;
    if (len > n) len = (int)n;
    kern_copy(buf, tmp, (size_t)len);
    return len;
}

// kern_str_parse_f64 is the correctly rounded value of text already
// checked to be one JSON number (±Inf when it overflows). strtod needs a
// NUL-terminated string: the text is copied to the stack when it fits and
// to the heap otherwise, where a failed allocation traps oom at the call.
double kern_str_parse_f64(const char *p, int64_t n, const kern_loc *at) {
    char small[1024];
    char *s = small;
    if (n < 0 || n >= (int64_t)sizeof small) {
        if (n < 0 || (uint64_t)n >= SIZE_MAX) kern_trap_loc(9, at);
        s = malloc((size_t)n + 1);
        if (s == NULL) kern_trap_loc(9, at);
    }
    kern_copy(s, p, (size_t)n);
    s[n] = 0;
    double v = strtod(s, NULL);
    if (s != small) free(s);
    return v;
}
