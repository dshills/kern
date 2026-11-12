BEGIN { x = 1 &&
  2
  y = 0 ||
  3
  print x, y,
     "after comma"
  z = 1 + \
      2
  print z
  s = "abc" \
      "def"
  print s
}
