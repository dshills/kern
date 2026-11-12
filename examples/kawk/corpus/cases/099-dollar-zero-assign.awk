NR == 1 { $0 = "x y z"; print NF, $2; $0 = ""; print NF; $0 = "  a   b  "; print NF, $1, $2; print "[" $0 "]" }
NR == 2 { $0 = $2 " " $1; print $1, NF }
