BEGIN { printf "%d\n", 1 > "f"; printf("%d\n", 2) >> "f"; close("f"); while ((getline l < "f") > 0) print "f:", l }
