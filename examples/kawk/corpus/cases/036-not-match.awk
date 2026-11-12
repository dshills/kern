BEGIN { x = "abc"; print !x ~ "0", !(x ~ "0"), x !~ "b", !x ~ 0, (!x) ~ "^0$" }
