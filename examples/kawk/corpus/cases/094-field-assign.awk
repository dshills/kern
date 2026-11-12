NR <= 6 { $2 = "X"; print; print NF }
NR == 7 { $1 = $1; print; $3 = ""; print; print NF }
