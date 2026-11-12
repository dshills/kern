BEGIN { a["old"] = 1; n = split("x y z", a); print n, length(a), ("old" in a), a[1], a[3]; n = split("", a); print n, length(a); split("q", a, ""); print a[1], length(a) }
