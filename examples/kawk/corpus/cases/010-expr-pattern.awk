NF > 5 { print NF }
$1 ~ /^[a-z]+$/ && NF == 2 { print "two:", $0 }
!NF { print "empty at", NR }
