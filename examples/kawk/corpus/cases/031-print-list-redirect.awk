BEGIN { print "a", "b" > "f"; print("c", "d") > "f"; printf("%s-%s\n", "e", "f") > "f"; close("f")
  while ((getline l < "f") > 0) print l }
