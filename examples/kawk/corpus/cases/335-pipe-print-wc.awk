{ print | "wc -l | tr -d ' '" } END { close("wc -l | tr -d ' '"); print "counted" }
