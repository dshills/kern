BEGIN { a[1, 2] = 3; for (k in a) { n = split(k, p, SUBSEP); print n, p[1], p[2] }; SUBSEP = ":"; a["x", "y"] = 1; print ("x:y" in a), (("x", "y") in a); b[1,2,3] = 1; for (k in b) print k }
