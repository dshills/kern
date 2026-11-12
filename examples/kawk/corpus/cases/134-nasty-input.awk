{ print NR, NF, length($0), length($1), length($NF) }
length($0) < 100 { print "[" $0 "]"; print "<" $1 ">" }
