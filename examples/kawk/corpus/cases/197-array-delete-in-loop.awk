BEGIN { for (i = 1; i <= 20; i++) a[i] = i; for (k in a) delete a[k]; print length(a); for (k in a) print "never"; a["z"]; for (k in a) print k }
