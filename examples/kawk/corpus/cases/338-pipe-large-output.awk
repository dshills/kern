BEGIN { for (i = 0; i < 20000; i++) print "line number", i | "wc -c | tr -d ' '"; close("wc -c | tr -d ' '"); n = 0; while (("seq 1 20000" | getline v) > 0) n += v; print n }
