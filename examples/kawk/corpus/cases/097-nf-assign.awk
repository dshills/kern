NR == 1 { NF = 2; print; print NF }
NR == 2 { NF = 7; print; print NF; NF = 0; print "[" $0 "]"; print NF }
NR == 10 { NF = NF; print; $0 = $0; print NF }
NR == 11 { NF -= 2; print; NF++; print $0 "|" }
