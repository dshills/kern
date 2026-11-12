/the/ { print NR, $0 } /^[0-9]+ / { print "num:", $1 }
