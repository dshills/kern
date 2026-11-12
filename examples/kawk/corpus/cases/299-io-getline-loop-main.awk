NR == 1 { while ((getline line) > 0) if (line ~ /dup/) d++; print d, NR }
