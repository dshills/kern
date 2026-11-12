/^#/ { comments++; next }
/start/, /stop/ { print "in range:  " $0; next }
$2 >= 10 && $1 ~ /^[a-c]/ { print "big a-c:   " $1, $2 }
$2 < 10
NR == 2, NR == 4 { early++ }
END { print comments " comments, " early " early, " NR " lines" }
