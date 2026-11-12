BEGIN { OFS = "-"; ORS = "|\n" } NR <= 3 { print $1, $2; print; $1 = $1; print } END { ORS = ""; print "a", "b"; print "c"; printf "\n" }
