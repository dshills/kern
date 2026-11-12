/^the/ { print "start:", $0 } /dog\.$/ { print "end:", $0 } /^end$/ { print "exact" } /^ / { sp++ } / $/ { tr++ } END { print sp, tr }
