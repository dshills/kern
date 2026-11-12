BEGIN { $0 = "a b"; $2 = "10.0"; print ($2 == 10); $2 = 1 / 3; print ($2 == 1 / 3), $2; $2 = "0"; if ($2) print "true"; else print "false"; $1 = u; if ($1 == 0) print "numlike"; print NF, $0 }
