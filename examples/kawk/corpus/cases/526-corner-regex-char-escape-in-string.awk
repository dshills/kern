BEGIN { print "a.b" ~ "a\\.b", "axb" ~ "a\\.b", "a\\b" ~ "a\\\\b", "a/b" ~ "a/b", "a+" ~ "a\\+", "tab\there" ~ "\t", "x" ~ "^[^\\]]$" }
