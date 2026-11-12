function f(NRx, n) { n = NR; return n } function g(i) { i++; return i } BEGIN { i = 10; print g(i), i, g(g(i)) } END { print f() }
