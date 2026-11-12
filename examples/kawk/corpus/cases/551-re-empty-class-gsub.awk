{ s = $0; n = gsub(/[z-a]/, "-", s); print n, s; t = $0; sub(/[z-a]/, "<&>", t); print t }
