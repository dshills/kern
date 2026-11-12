BEGIN { a = 10; b = 2; c = 5
  print a / b / c
  print a /b/ c
  x = 6 / 2 ; print x
  n = 100; n /= 5; print n
  $0 = "a/b"; if (/a\/b/) print "slash matched"
  print 1 / 4, 8 / 2 / 2
  print (a) / (b)
  print a/b
}
/=/ { print "has equals:", $0 }
