{ i = 1; print $i, $(i + 1), $NF, $(NF > 2 ? 3 : 1); j = NF; print $j--, j }  NR == 2 { print $$1, $$$2, $($1 + $2), $-0 }
