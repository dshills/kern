BEGIN { print "a" > "f"; close("f"); print "b" >> "f"; print "c" >> "f"; close("f")
  while ((getline l < "f") > 0) print l }
