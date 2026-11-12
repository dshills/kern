BEGIN { split("the quick brown fox jumps over the lazy dog", w, " "); for (i in w) print i, w[i]; for (i = 1; i <= 9; i++) c[w[i]]++; for (k in c) print k, c[k] }
