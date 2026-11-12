BEGIN { for (i = 0; i <= 20; i++) printf "%g ", sqrt(i); print ""; print sqrt(2), sqrt(1e10), sqrt(1e-10), sqrt(0.25), sqrt(144), sqrt(2 ^ 60), sqrt(1e300), sqrt(1e-300), sqrt(-1), sqrt("16"); printf "%.15g %.15g %.15g\n", sqrt(2), sqrt(3), sqrt(1e15 + 1)
  for (i = 1; i <= 2000; i += 37) if (sqrt(i * i) != i) print "inexact", i; print "squares ok" }
