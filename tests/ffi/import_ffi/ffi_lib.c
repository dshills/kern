#include <stdint.h>
#include <stddef.h>
size_t lib_fill(uint8_t *p, size_t n, uint8_t v) { for (size_t i = 0; i < n; i++) p[i] = (uint8_t)(v + i); return n; }
uint64_t lib_sum(const uint8_t *p, size_t n) { uint64_t s = 0; for (size_t i = 0; i < n; i++) s += p[i]; return s; }
