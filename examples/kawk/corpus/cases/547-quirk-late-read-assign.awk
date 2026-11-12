BEGIN { i = 5; c[i++] = i; print c[5], i; d[i] = i++; print d[6] "|" d[7] "|" i; i = 1; a[i++] += i; for (k in a) print k, a[k]; r = 1; e[1] = 5; e[r] += r++; print e[1], e[2]
  $0 = "10 20 30"; f = 1; $f = f++; print $0, f; g = 1; $(g++) += g; print $0, g; h[1] = "x"; m = 1; h[m] = h[m] m++; print h[1], m }
