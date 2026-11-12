BEGIN { s = "banana"; print gsub(/a/, "A", s), s, gsub(/n/, "", s), s, sub(/A/, "", s), s, gsub(/x/, "y", s), sub(/^/, "^", s), s, gsub(/./, "&&", s), s, length(s) }
