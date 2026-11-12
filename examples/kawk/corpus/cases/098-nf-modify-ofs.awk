BEGIN { OFS = "-" } NR <= 2 { $1 = $1; print; print $1, $2 } NR == 4 { NF = 3; print } NR == 5 { $3 = "z"; print }
