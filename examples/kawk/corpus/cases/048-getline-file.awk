BEGIN { print "one\ntwo\nthree" > "f"; close("f")
  while ((getline line < "f") > 0) print "L:", line, NR, NF
  close("f"); getline < "f"; print $0, NF, NR; getline a["x"] < "f"; print a["x"]
  getline $2 < "f"; print $0, NF
  print (getline q < "/no/such/file"), (getline < "/no/such/file2") }
