BEGIN { a[1]; a["x y"]; print 1 in a, 2 in a, "x" " " "y" in a, (1 in a) + (2 in a), 1 in a && 2 in a, !(1 in a), 1 in a ? "y" : "n" }
