{ $1 = "X"; n = NF; NF = n + 1; print; $NF = "end"; print } NR == 2 { exit }
