// Host driver for the freestanding float-text differential test
// (specs/freestanding-float R5). It links f64text.c and answers one line
// per request on stdin:
//   D <hex bits>  ->  <freestanding digits> <libc digits>
//   P <text>      ->  <freestanding bits> <libc bits>   (hex)
// The libc side is the hosted runtime's code (runtime/hosted/src/kern_rt.c)
// with its names changed. Neither side calls setlocale or fesetround, so
// libc runs in the C locale with round-to-nearest.
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int64_t kern_str_f64_digits(double v, char *buf, int64_t n);
double kern_str_parse_f64(const char *p, int64_t n, const void *at);

static int64_t ref_digits(double v, char *buf, int64_t n) {
    char tmp[40];
    int len = 0;
    for (int p = 1; p <= 17; p++) {
        len = snprintf(tmp, sizeof tmp, "%.*e", p - 1, v);
        if (strtod(tmp, NULL) == v) break;
    }
    if (n < 0) n = 0;
    if (len > n) len = (int)n;
    memcpy(buf, tmp, (size_t)len);
    return len;
}

static uint64_t bits(double v) { uint64_t u; memcpy(&u, &v, 8); return u; }

int main(void) {
    size_t cap = 0;
    char *line = NULL;
    ssize_t got;
    while ((got = getline(&line, &cap, stdin)) > 0) {
        if (line[got - 1] == '\n') line[--got] = 0;
        if (got < 2) continue;
        if (line[0] == 'D') {
            uint64_t u = strtoull(line + 2, NULL, 16);
            double v;
            memcpy(&v, &u, 8);
            char a[40], b[40];
            int64_t na = kern_str_f64_digits(v, a, sizeof a);
            int64_t nb = ref_digits(v, b, sizeof b);
            printf("%.*s %.*s\n", (int)na, a, (int)nb, b);
        } else if (line[0] == 'P') {
            const char *s = line + 2;
            int64_t n = (int64_t)strlen(s);
            double a = kern_str_parse_f64(s, n, NULL);
            double b = strtod(s, NULL);
            printf("%016llx %016llx\n", (unsigned long long)bits(a), (unsigned long long)bits(b));
        } else if (line[0] == 'T') {
            // truncation: D with a buffer of n bytes; prints both lengths
            // and contents
            uint64_t u = 0;
            long long n = 0;
            sscanf(line + 2, "%llx %lld", (unsigned long long *)&u, &n);
            double v;
            memcpy(&v, &u, 8);
            char a[40] = {0}, b[40] = {0};
            int64_t na = kern_str_f64_digits(v, a, (int64_t)n);
            int64_t nb = ref_digits(v, b, (int64_t)n);
            printf("%lld:%.*s %lld:%.*s\n", (long long)na, (int)(na > 0 ? na : 0), a, (long long)nb, (int)(nb > 0 ? nb : 0), b);
        }
    }
    free(line);
    return 0;
}
