{ if (NR == 1) { print "one" }; if (NR == 2) { print "two" } ; }
END { for (i = 0; i < 2; i++) { print i }; print "done" }
