BEGIN { a["x"] = 1; a[1,2] = 3; print ("x" in a), ("y" in a), ((1,2) in a), ((2,1) in a)
  if ("x" in a && !("z" in a)) print "ok"; n = 0; for (k in a) n++; print n; print length(a)
  k = "x"; print k in a; print (k in a) ? "in" : "out"; print !(k in a) }
