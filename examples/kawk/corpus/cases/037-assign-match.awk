BEGIN { b = "hello"; a = b ~ "ell"; print a; a = b ~ /z/; print a; c = b !~ /z/ ; print c }
