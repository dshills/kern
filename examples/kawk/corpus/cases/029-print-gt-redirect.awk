BEGIN { print 1 > "out1"; print 2 > "out1"; close("out1")
  while ((getline line < "out1") > 0) print "got", line
  print (1 > 2), (2 > 1)
  print (3 > 2) ? "yes" : "no"
  x = 5; print x > "out2"; close("out2"); getline y < "out2"; print "y=" y }
