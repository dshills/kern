BEGIN { print "first"; print "b\na\nc" | "sort"; print "second"; close("sort"); print "third"; print "y\nx" | "sort -r"; print "fourth" }
