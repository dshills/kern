BEGIN { for (i = 0; i < 100; i++) a[i] = i * i; n = 0; s = 0; for (k in a) { n++; s += a[k] }; print n, s; for (k in a) if (k % 2) delete a[k]; n = 0; for (k in a) n++; print n, length(a) }
