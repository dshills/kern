BEGIN { c = "echo hi"; c | getline a; r = (c | getline b); print a, r; close(c); c | getline b; print b; print close(c), close(c) }
