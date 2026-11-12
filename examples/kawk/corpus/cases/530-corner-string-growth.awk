BEGIN { s = "x"; for (i = 0; i < 16; i++) s = s s; print length(s); n = gsub(/x/, "yz", s); print n, length(s), substr(s, 1, 6); t = sprintf("%100000s", "end"); print length(t), substr(t, 99998) }
