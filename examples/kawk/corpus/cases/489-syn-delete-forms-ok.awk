BEGIN { a[1]; a[2]; a[1, 2]; a["x"]; delete a[1]; delete a[1, 2]; delete a["x"]; print length(a); delete a; print length(a); a[5] = 1; i = 5; delete a[i]; print length(a) }
