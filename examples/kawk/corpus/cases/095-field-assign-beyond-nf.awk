NR == 1 { $7 = "seven"; print; print NF; $(NF + 2) = "x"; print; print NF }
NR == 3 { $3 = "c"; print; print NF, length($0) }
