BEGIN { for (i = 0; i < 5; i++) s = s i; print s
  for (;;) { n++; if (n > 3) break }; print n
  for (i = 10; i > 0; i -= 3) printf "%d ", i; print ""
  for (i = 0; i < 10; i++) { if (i % 2) continue; t = t i }; print t
  for (i = 1; i <= 3; i++)
      for (j = 1; j <= 3; j++)
          if (i != j) printf "%d%d ", i, j
  print "" }
