BEGIN { s = "abc"; print s ~ "b" "c", s ~ "b" && 1, (s ~ "x") || (s ~ "a"), !(s ~ "a"), s ~ "a" ? "y" : "n", 1 + (s ~ /b/); x = s ~ /a/ && s ~ /c/; print x }
