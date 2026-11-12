BEGIN { for (r = 0; r < 5; r++) { for (i = 0; i < 300; i++) a[i] = r; delete a; if (length(a)) print "bad" }; a[1] = "ok"; print a[1], length(a) }
