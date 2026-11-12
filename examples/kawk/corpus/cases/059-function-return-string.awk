function rep(s, n,   r) { while (n-- > 0) r = r s; return r }
BEGIN { print rep("ab", 3), length(rep("x", 10)), rep("z", 0) "|" }
