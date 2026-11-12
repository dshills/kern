function f(a,   i, s) { for (i = 0; i < a; i++) s = s i; return s }
BEGIN { i = 99; s = "keep"; print f(4), i, s; print f(2) }
