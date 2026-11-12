BEGIN { s = "hello world"; gsub(/o/, "[&]", s); print s; t = "abc"; gsub(/b/, "&&", t); print t; u = "abc"; gsub(/b/, "\\&", u); print u; v = "abc"; gsub(/b/, "\\\\&", v); print v
  w = "abc"; gsub(/b/, "\\\\\\&", w); print w; x = "abc"; gsub(/b/, "x\\y", x); print x; y = "abc"; gsub(/b/, "x\\\\y", y); print y; z = "a&b"; gsub(/&/, "and", z); print z; q = "abc"; sub(/b/, "[\\\\&]", q); print q }
