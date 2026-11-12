function bump(a, k) { a[k]++ ; return a[k] } BEGIN { print bump(c, "x"), bump(c, "x"), bump(c, "y"), c["x"], c["y"] }
