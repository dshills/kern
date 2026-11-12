BEGIN { print "f1 f2 f3\nsecond" > "f"; close("f") } NR == 1 { getline < "f"; print $0, NF, NR, FNR; getline v < "f"; print v, NF, NR, $0 }
