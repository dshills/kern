BEGIN { print "b1" } BEGIN { print "b2" } END { print "e1", NR } END { print "e2" } { n++ }
END { print n }
