{ n += length } NF { m += length $1 } END { print n, m; if (length > 2) print "long" }
