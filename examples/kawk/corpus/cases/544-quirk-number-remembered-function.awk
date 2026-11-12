function use(p) { return p + 1 } function test(p) { return p ? "T" : "F" } function both(p,  t) { t = p + 0; return p ? "T" : "F" }
BEGIN { v = "0"; r = use(v); print test(v), both(v), test(v); w = "0"; w += 0; print test(w), w; a[1] = "0.0"; print both(a[1]), test(a[1]) }
