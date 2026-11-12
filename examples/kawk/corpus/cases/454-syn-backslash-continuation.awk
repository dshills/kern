BEGIN { x = 1 + \
2 + \
3; print x; if (x == 6 && \
  x > 0) print \
  "yes"; s = "a" \
 "b"; print s }
NR == 1 \
  { print "first" }
