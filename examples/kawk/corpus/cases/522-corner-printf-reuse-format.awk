{ fmt = "%-" (3 + NR) "s|%0" NR "d\n"; printf fmt, $1, NR } NR == 4 { exit }
