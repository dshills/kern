BEGIN { x = 3.14159265; OFMT = "%.2f"; print x, x "", 17, 17.5, 1e6, 1e6 + 0.5; OFMT = "%e"; print x, 100; print x + 0 "" ; OFMT = "%.10g"; print 1 / 3, 2 / 3 }
