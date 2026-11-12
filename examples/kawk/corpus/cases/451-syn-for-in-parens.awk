BEGIN { a["x"]; a["y"]; n = 0; for (k in a) n++; for ( k in a ) n++; for (k in a) { n++ } print n; for ((k) in a) m++; print m }
