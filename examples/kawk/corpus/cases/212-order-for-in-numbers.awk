BEGIN { for (i = 100; i > 0; i--) a[i] = 1; for (k in a) printf "%s ", k; print ""; delete a[50]; a[50] = 2; a[1000] = 3; for (k in a) printf "%s ", k; print "" }
