BEGIN { print "v1" > "f"; close("f"); getline a < "f"; close("f"); print "v2" > "f"; close("f"); getline b < "f"; print a, b }
