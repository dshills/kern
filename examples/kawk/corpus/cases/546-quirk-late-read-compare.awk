BEGIN { i = 1; print (i == i++), i; i = 1; print (i++ == i), i; n = 3; print (n < n++), n; s = "a"; print (s == (s = "b")), s; b[1] = 7; q = 1; print (b[q] == b[q]++), b[1]
  $0 = "1 2 3"; t = 1; print ($t == $t++), $0; j = 5; print (j > j--), (j >= j--), j; k = 2; print (k != (k += 0)), (k != (k += 1)) }
