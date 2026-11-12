BEGIN { for (i = 0; i < 200000; i++) { s += i; if (i % 3 == 0) t++ }; print s, t; while (j < 100000) j += 0.5; print j }
