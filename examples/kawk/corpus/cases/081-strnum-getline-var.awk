BEGIN { print "10\n9\nabc\n 7 " > "f"; close("f"); getline a < "f"; getline b < "f"; getline c < "f"; getline d < "f"
  print (a > b), (a c), (c < a), (d == 7), (d == " 7 "), (d "" == 7) }
