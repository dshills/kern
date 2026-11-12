BEGIN { print "error text" | "cat 1>&2"; close("cat 1>&2"); print "ok" }
