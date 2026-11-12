NR == 2 { getline; print NR, $1, NF; $2 = "X"; print; getline; print NR, $0 }
