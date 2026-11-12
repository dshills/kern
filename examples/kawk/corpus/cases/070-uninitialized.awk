BEGIN { print x "|" x + 0 "|" length(x) "|" (x == 0) "|" (x == "") "|" (!x); if (x) print "t"; else print "f"; y[1]; print length(y), y[1] "|" }
