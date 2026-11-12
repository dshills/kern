BEGIN { # c1
  x = 1 # c2
  if (x) # c3
    print "x" # c4
  else # c5
    print "y"
  for (i = 0; i < 2; i++) # c6
    print i
  y = x + # c7
      2
  print y
} # c8
# c9
