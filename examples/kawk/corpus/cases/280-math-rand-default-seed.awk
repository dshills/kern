BEGIN { for (i = 0; i < 5; i++) printf "%.6f ", rand(); print ""; n = 0; for (i = 0; i < 1000; i++) { r = rand(); if (r < 0 || r >= 1) bad++; if (r < 0.5) n++ }; print n, bad + 0 }
