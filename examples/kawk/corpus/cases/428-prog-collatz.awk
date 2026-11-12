function steps(n,  s) { while (n != 1) { n = n % 2 ? 3 * n + 1 : n / 2; s++ }; return s + 0 } BEGIN { for (i = 1; i <= 30; i++) printf "%d ", steps(i); print ""; print steps(837799) }
