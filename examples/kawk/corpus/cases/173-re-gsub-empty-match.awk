BEGIN { s = "aaa"; print gsub(/x*/, "-", s), s; s = "abc"; print gsub(/b*/, "-", s), s; s = "abc"; print gsub(//, "-", s), s; s = ""; print gsub(/x*/, "-", s), "[" s "]"; s = "baaac"; print gsub(/a*/, "X", s), s
  s = "abc"; print gsub(/$/, "!", s), s; s = "abc"; print gsub(/^/, "!", s), s; s = "a b c"; print gsub(/ */, "_", s), s; s = "hello"; print gsub(/l*/, "L", s), s }
