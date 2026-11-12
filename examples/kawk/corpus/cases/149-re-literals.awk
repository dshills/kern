/fox/ { print "fox", NR } /dup/ { d++ } /^$/ { e++ } /\./ { dot++ } /a.c/ { n++ } END { print d, e, dot, n }
