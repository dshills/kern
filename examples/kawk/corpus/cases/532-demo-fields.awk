{ print NR ": " NF " fields, first [" $1 "] last [" $NF "]" }
NR == 2 { $2 = "CHANGED"; print; print NF }
NR == 3 { $6 = "sixth"; print; NF = 2; print; print NF }
NR == 4 { print "empty record: NF is " NF " and $1 is [" $1 "]" }
END { OFS = "-"; $0 = "a b c"; $1 = $1; print; print $(NF - 1) $NF; print NR " records" }
