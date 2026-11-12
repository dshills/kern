BEGIN { "echo " "a" " b" | getline x; print x; "echo 5" | getline n; print n + 1; r = "echo z" | getline z; print r, z; if (("echo q" | getline q) > 0) print q }
