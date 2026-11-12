/fox|dog/ { a++ } /^(alpha|name:bob)/ { print $0 } /(a|b)(c|d)/ { b++ } /^(the |The )+/ { c++ } /x|y|z/ { d++ } END { print a, b, c, d }
