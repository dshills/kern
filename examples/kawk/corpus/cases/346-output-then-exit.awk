BEGIN { print "x" > "f"; print "y" | "cat"; exit 4 } END { print "end still runs" }
