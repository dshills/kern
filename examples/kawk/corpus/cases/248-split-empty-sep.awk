BEGIN { n = split("hello", c, ""); print n, c[1], c[5]; n = split("a b", c, ""); print n, "[" c[2] "]"; n = split("", c, ""); print n }
