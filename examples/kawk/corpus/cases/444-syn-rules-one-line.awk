BEGIN { a = 1 } ; BEGIN { b = 2 } END { print a, b, NR }; END { print "x" }
