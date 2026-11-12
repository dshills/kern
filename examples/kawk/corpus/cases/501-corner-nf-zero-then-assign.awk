BEGIN { $0 = "a b c"; NF = 0; print "[" $0 "]", NF; $2 = "x"; print "[" $0 "]", NF; NF = 1; print "[" $0 "]"; $1 = ""; print "[" $0 "]", NF }
