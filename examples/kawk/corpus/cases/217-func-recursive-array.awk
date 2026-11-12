function walk(n, acc) { if (n == 0) return; acc[n] = n * n; walk(n - 1, acc) } BEGIN { walk(10, sq); s = 0; for (k in sq) s += sq[k]; print s, length(sq) }
