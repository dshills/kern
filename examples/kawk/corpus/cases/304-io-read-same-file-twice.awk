BEGIN { print "x\ny" > "f"; close("f"); getline a < "f"; close("f"); getline b < "f"; getline c < "f"; print a, b, c }
