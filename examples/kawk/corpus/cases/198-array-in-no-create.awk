BEGIN { if ("k" in a) print "yes"; print length(a); x = a["k"]; print length(a); if (("k" in a) && a["k"] == "") print "now there"; if (a["q"] == 0) print length(a) }
