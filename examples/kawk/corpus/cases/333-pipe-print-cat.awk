BEGIN { print 1; print 2 | "cat"; print 3; close("cat"); print 4; printf "%s\n", 5 | "cat"; print 6 }
