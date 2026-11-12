BEGIN { print NR, OFS; print "a", "b" } { print $2 }
