function f(n,   loc) { loc[n] = n; return length(loc) } BEGIN { print f(1), f(2), f(3) }
