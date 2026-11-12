BEGIN { a["x"] = 1; a["y"] = "two"; a[3] = 3.5; print a["x"], a["y"], a[3], a["3"], a[3.0], length(a); a["x"] = "changed"; print a["x"]; print a["missing"] "|", length(a) }
