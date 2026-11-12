BEGIN { "echo 10 20 30" | getline; print $2, NF; s = 0; cmd = "printf '1\\n2\\n3\\n'"; while ((cmd | getline n) > 0) s += n; close(cmd); print s; cmd | getline first; print first }
