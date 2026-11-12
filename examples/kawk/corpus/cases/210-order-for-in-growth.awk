BEGIN { for (i = 0; i < 1000; i++) a["k" i] = i; n = 0; for (k in a) { if (n < 25 || n > 990) printf "%s ", k; n++ }; print ""; for (i = 0; i < 1000; i += 3) delete a["k" i]; n = 0; for (k in a) { if (n++ < 25) printf "%s ", k }; print ""
  for (i = 0; i < 400; i++) a["z" i] = 1; n = 0; for (k in a) { if (n++ < 40) printf "%s ", k }; print "" }
