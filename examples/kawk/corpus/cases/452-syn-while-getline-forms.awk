BEGIN { print "a\nb\nc" > "f"; close("f"); while ((getline l < "f") > 0) n++; close("f"); while (getline l < "f") m++; close("f"); while ((getline < "f") > 0) k += NF; print n, m, k }
