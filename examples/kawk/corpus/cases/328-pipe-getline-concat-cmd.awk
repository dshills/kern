BEGIN { n = 3; "echo " n " items" | getline r; print r; "echo " "x" | getline; print; while ("seq 1 3" | getline v > 0) t = t v; print t }
