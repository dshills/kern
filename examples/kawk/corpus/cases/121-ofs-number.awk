BEGIN { OFS = 5; print "a", "b"; ORS = 0.5; print "c"; ORS = "\n"; print "" }
