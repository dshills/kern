BEGIN { pi = atan2(0, -1); for (i = 0; i <= 16; i++) printf "%.6f %.6f | ", sin(i * pi / 8), cos(i * pi / 8); print ""; for (x = -10; x <= 10; x += 1.3) printf "%g %g | ", sin(x), cos(x); print ""
  print sin(0), cos(0), sin(1e6), cos(1e6), sin(1e9), cos(1e9), sin(1e12), cos(1e12), sin(-0.001), cos(1e-10); printf "%.12g %.12g %.12g\n", sin(1), cos(1), sin(100) }
