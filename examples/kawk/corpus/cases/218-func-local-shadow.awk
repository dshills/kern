function f(x) { x = 10; y = 20; return x } BEGIN { x = 1; y = 2; print f(5), x, y }
