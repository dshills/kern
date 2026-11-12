BEGIN { print "x" > "f"; close("f"); if ((getline l < nofile) < 0) print "neg"; else print "other" }
