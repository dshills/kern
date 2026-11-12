BEGIN { print "abc" ~ //, "" ~ //, match("abc", //), RSTART, RLENGTH; s = "abc"; print gsub(//, "-", s), s; print "x" ~ /()/, "x" ~ /^*x/, "aa" ~ /^a**$/ }
