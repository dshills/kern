#include <stdint.h>
#include <stdbool.h>
int64_t c_add(int64_t a, int64_t b) { return a + b; }
double c_scale(double x, uint32_t k) { return x * k; }
bool c_flag(bool b) { return !b; }
uint64_t kern_double(uint64_t x);
uint64_t c_call_twice(uint64_t x) { return kern_double(kern_double(x)); }
