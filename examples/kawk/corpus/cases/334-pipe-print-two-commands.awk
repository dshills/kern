BEGIN { print "z\na" | "sort"; print "A\nB" | "tr A-Z a-z"; close("tr A-Z a-z"); close("sort"); print "done" }
