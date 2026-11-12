BEGIN { for (i = 0; i < 3; ) i++; print i; for (; i < 6; i++) ; print i; i = 0; for (;;) if (++i > 4) break; print i
  for (i = 0; i < 9; i += 3) for (j = i; j < i + 2; j++) printf "%d%d ", i, j; print ""
  for (k = (1 in a); k < 2; k++) print "k", k }
