BEGIN { CONVFMT = "%.2g"; a[0.123456] = 1; a[12] = 2; a[1e10] = 3; for (k in a) n++; print n, (0.12 in a), ("0.12" in a), (12 in a), ("1e+10" in a), ("10000000000" in a) }
