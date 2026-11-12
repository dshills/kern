function f(a,
           b,
           c) { return a b c }
BEGIN { print f(1,
  2,
  3); printf "%s %s\n",
    "x",
    "y"; a[1,
    2] = 3; print a[1, 2] }
