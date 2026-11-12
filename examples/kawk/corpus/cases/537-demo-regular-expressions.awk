BEGIN {
    s = "The year 1999 and the year 2024"
    print match(s, /[0-9]+/), RSTART, RLENGTH, substr(s, RSTART, RLENGTH)
    print match(s, /x+/), RSTART, RLENGTH
    print match("xabcabcabcy", /(abc)+/), RLENGTH, match("foobar", /foo|foobar/), RLENGTH
    t = s; n = gsub(/[0-9]+/, "<&>", t); print n, t
    t = s; sub(/year/, "month", t); print t
    t = s; gsub(/the|The/, "", t); gsub(/  +/, " ", t); sub(/^ /, "", t); print t
    t = "a.b.c"; gsub(/\./, "\\&", t); print t
    t = "banana"; print gsub(/a/, "&&", t), t, gsub(/x*/, "-", t), t
    n = split("2024-11-12T08:30:00", p, /[-T:]/); print n, p[1], p[3], p[6]
    n = split("a1b22c333d", q, /[0-9]+/); print n, q[1] q[2] q[3] q[4]
    pat = "^[A-Z][a-z]+$"; print ("Kern" ~ pat), ("kern" ~ pat), ("KERN" !~ pat)
    print ("aaa" ~ /^a{2,3}$/), ("aaaa" ~ /^a{2,3}$/), ("x9" ~ /^[[:alpha:]][[:digit:]]$/)
}
/[aeiou][aeiou]/ { print "two vowels together: " $0 }
$0 ~ "^.[^ ]* [a-z]" && !/q/ { gsub(/[^a-z]/, ""); print "letters: " $0 }
