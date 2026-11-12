BEGIN { r = "^[0-9]+$"; print "123" ~ r, "12a" ~ r, "a.c" ~ "a\\.c", "abc" ~ "a\\.c", "abc" ~ "a.c", "a+b" ~ "a\\+b", "aab" ~ "a+b"
  p = "o+"; s = "foo boo"; n = gsub(p, "0", s); print n, s; print match("xxabcxx", "a" "b" "c"), RSTART, RLENGTH; x = "b|c"; print "c" ~ x, "d" ~ x; print "5" ~ 5, 15 ~ 5, 15 ~ "^5" }
