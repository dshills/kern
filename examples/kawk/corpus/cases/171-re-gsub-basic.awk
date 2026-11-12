{ n = gsub(/[aeiou]/, "#"); print n, $0 } END { s = "a.b.c"; gsub(/\./, "-", s); print s; gsub(/-/, "", s); print s; print gsub(/x/, "y", s), s }
