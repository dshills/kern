BEGIN {
  i = 0
  do
    i++
  while (i < 3)
  if (i == 3) print "three"
  else
    print "not"
  do { i-- } while (i)
  print i
}
