#include <stdint.h>
#include <stddef.h>
size_t c_fill(uint8_t *p, size_t n, uint8_t v) { for (size_t i = 0; i < n; i++) p[i] = v; return n; }
uint64_t c_sum(const uint8_t *p, size_t n) { uint64_t s = 0; for (size_t i = 0; i < n; i++) s += p[i]; return s; }
