BEGIN { print "x" > "f"; print close("f"), close("f"), close("never-opened"); getline l < "f"; print close("f"), l; print close("/dev/null/nope") }
