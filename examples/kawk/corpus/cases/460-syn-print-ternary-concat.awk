BEGIN { x = 1; print x ? "t" : "f", x ? "T" : "F"; print (x ? "a" : "b") (x ? "c" : "d"); print x ? "a" "b" : "c"; print x == 1 ? "eq" : "ne" }
