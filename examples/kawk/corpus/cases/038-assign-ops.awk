BEGIN { x = 10; x += 5; print x; x -= 3; print x; x *= 2; print x; x /= 4; print x; x %= 4; print x; x ^= 3; print x; x **= 2; print x
  a = b = c = 7; print a, b, c; a += b += c; print a, b, c
  print (y = 5) + 1, y; z = (w = 2) * 3; print z, w }
