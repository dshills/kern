function f(a, b) { return a + b } BEGIN { print f(x = 2, y = x * 3), x, y; print length(s = "hello"), s; print substr(t = "abcdef", n = 2, 3), t, n }
