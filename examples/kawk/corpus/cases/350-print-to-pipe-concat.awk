BEGIN { cmd = "cat"; print "hello" | cmd; close(cmd); print "a" | "c" "at"; close("cat"); print "done" }
