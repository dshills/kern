function two(a, b) { return a + b } BEGIN { i = 1; print two(i++, i++), i; print two(n = 5, n * 2), n }
