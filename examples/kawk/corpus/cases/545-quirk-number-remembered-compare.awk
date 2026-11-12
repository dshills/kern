BEGIN { x = "10"; y = "9"; print (x < y); s = x + y; print (x < y), (x == 10), (x == "10.0"), (x "" < y ""); z = "1e1"; print (z == 10); t = z * 1; print (z == 10), (z == "1e1"), z }
