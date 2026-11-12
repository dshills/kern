BEGIN { x = 1 < 2; y = 2 < 1; z = (1 < 2) + (3 > 2) + (2 == 2); print x, y, z; w = "a" "b" == "ab"; print w; v = 1 + 1 == 2; print v }
