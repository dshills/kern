BEGIN { print match("abcd", /abcd|bc/), RLENGTH; print match("xabcd", /bc|abcd|a/), RSTART, RLENGTH; print match("abcabc", /(a|ab)(c|bcd)?/), RLENGTH; print match("aaaa", /a|aa|aaa/), RLENGTH
  print match("xyz", /y*/), RSTART, RLENGTH; print match("foobar", /(foo|foobar)/), RLENGTH; print match("ab", /(a|ab)(b)?/), RLENGTH; print match("abab", /(ab)*/), RLENGTH; print match("xxaby", /a?b?y?$/), RSTART, RLENGTH
  s = "aaa bbb"; sub(/a*|b+/, "X", s); print s; s = "xyz"; sub(/y|yz/, "-", s); print s; s = "abcde"; sub(/b|bc|bcd/, "<&>", s); print s }
