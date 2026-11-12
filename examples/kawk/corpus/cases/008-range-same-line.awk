/dup/, /dup/ { print NR }
NR == 2, NR == 4 { print "r", NR }
$1 == "10", $1 == "70" { print "t", $0 }
