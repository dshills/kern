BEGIN { print "to stdout" > "/dev/stdout"; print "to stderr" > "/dev/stderr"; printf "%s\n", "again" > "/dev/stdout"; print "plain" }
