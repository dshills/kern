BEGIN { x = 1; for (i = 0; i < 50; i++) x = (x + i) * 1.01 - (x - i) * 0.01; printf "%.6f\n", x; s = ""; for (i = 0; i < 30; i++) s = "(" s ")"; print length(s), s ~ /^\(+\)+$/ }
