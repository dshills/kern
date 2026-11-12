BEGIN { print "1 one\n2 two\n3 three" > "names"; close("names"); while ((getline l < "names") > 0) { split(l, p, " "); name[p[1]] = p[2] } } $1 in name { print $0, "->", name[$1] }
