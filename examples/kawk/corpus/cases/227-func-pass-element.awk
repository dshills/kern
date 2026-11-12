function inc(v) { v++; return v } BEGIN { a[1] = 5; print inc(a[1]), a[1]; print inc($1), inc(NR), inc("7") }
