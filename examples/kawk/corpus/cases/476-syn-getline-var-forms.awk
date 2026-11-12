BEGIN { print "v1\nv2\nv3\nv4" > "f"; close("f"); getline a < "f"; getline b[1] < "f"; getline $2 < "f"; i = 3; getline arr[i] < "f"; print a, b[1], $0, arr[3] }
