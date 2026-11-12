BEGIN { print "a\nb" > "f"; close("f"); print (getline x < "f"), x; print (getline x < "f"), x; print (getline x < "f"), x; print (getline x < "f"), x; print (getline y < "missing"), "[" y "]" }
