NR == 1 { print "first:", $0; getline; print "second:", $0, NR
  getline x; print "third in x:", x, NR, $0 }
NR == 5 { while ((getline line) > 0) n++; print "rest:", n, NR }
END { print NR, (getline z), z "|" }
