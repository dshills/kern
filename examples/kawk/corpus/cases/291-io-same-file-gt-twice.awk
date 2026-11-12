BEGIN { print "a" > "f"; print "b" > "f"; print "c" >> "f"; close("f"); while ((getline l < "f") > 0) printf "%s ", l; print "" }
