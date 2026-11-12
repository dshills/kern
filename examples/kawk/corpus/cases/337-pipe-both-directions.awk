BEGIN { print "3\n1\n2" > "nums"; close("nums"); while (("sort -n nums" | getline v) > 0) s = s v; print s; print "x" | "cat > copy"; close("cat > copy"); getline y < "copy"; print y }
