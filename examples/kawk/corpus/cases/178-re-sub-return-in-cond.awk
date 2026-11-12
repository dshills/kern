{ if (sub(/^name:/, "")) { c++; print } } END { print c; s = "x"; while (sub(/x/, "xy", s) && length(s) < 5) ; print s }
