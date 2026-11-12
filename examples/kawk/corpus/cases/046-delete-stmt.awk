BEGIN { a[1] = 1; a[2] = 2; a[3] = 3; delete a[2]; print length(a), (2 in a); delete a; print length(a)
  a["x"] = 5; print a["x"]; b[1,2] = 1; delete b[1,2]; print length(b); delete c; print length(c); delete a["nope"]; print length(a) }
