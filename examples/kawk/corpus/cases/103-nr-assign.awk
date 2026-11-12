NR == 2 { NR = 10 } NR == 12 { print "twelve:", $0; FNR = 100 } END { print NR, FNR }
