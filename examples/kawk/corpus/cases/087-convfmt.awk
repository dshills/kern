BEGIN { x = 3.14159265; print x, x ""; CONVFMT = "%.2f"; print x, x ""; y = x ""; print y; a[x] = 1; for (k in a) print k
  z = 17; print z "" ; CONVFMT = "%.3g"; print 1234.5 "", 0.00012345 "", 12 "" }
