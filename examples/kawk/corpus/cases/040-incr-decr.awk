BEGIN { x = 5; print x++, x, ++x, x, x--, x, --x, x
  a[1] = 10; print a[1]++, a[1], ++a[1], --a[1], a[1]--, a[1]
  $0 = "3 4 5"; print $1++, $1, ++$2, $0, $3--, $0
  i = 1; print $i++, i, $i; print $++i, i
  print x++ + ++x, x; print x-- - --x, x; y = 1; z = y++ + y++; print y, z }
