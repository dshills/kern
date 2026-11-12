BEGIN { a = b = c = 5; print a, b, c; a += b -= c *= 2; print a, b, c; x[1] = y = $1 = "v"; print x[1], y, $1, $0; n = (m = 3) + m; print n, m }
