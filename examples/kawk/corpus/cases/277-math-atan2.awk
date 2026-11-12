BEGIN { print atan2(0, 1), atan2(1, 0), atan2(0, -1), atan2(-1, 0), atan2(1, 1), atan2(-1, -1), atan2(1, -1), atan2(-1, 1), atan2(0, 0), atan2(5, 0.001), atan2(1e-10, 1), atan2(1e10, 1), atan2(3, 4), atan2("1", "2")
  for (y = -2; y <= 2; y += 0.8) for (x = -2; x <= 2; x += 0.9) printf "%.5f ", atan2(y, x); print ""; printf "%.12g %.12g\n", atan2(1, 1) * 4, atan2(0, -1) }
