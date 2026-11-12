BEGIN { RS = "" } NR == 1 { getline; print "second: " $1; getline x; print "third: " x }
