{ s = $0; c = 0; while (match(s, /[0-9]+/)) { c++; sum += substr(s, RSTART, RLENGTH); s = substr(s, RSTART + RLENGTH) }; if (c) print NR, c, sum }
