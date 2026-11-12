BEGIN { v = "0"; if (v) print "true first"; m = v * 1; if (v) print "still true"; else print "false now"; if (v == 0) print "equals 0"
  w = "abc"; n = w + 0; if (w) print "abc stays a string"; x = "0.0"; y = x + 1; print (x ? "T" : "F"), (x == 0), (x == "0.0"), x
  z = " 12 "; q = z - 2; print (z == 12), (z < 5), z "|" }
