BEGIN { print "start"; exit 3; print "not" } { print } END { print "end" }
