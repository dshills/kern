BEGIN { print "aaa" ~ /^(a*)*$/, "" ~ /^(a*)+$/, "b" ~ /^(a*)*b$/, "ab" ~ /^(a|b*)*$/, match("aabb", /(a*b*)*/), RLENGTH, match("xyz", /(a*)*/), RLENGTH, "ab" ~ /^(a?)*b$/ }
