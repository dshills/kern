BEGIN { n = split("10 9 abc", a); print (a[1] > a[2]), (a[1] < a[3]); n = split("1,2,3", b, ","); s = 0; for (i = 1; i <= n; i++) s += b[i]; print s; n = split("a1b", c, 1); print n, c[1], c[2] }
