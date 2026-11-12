END { r = getline; print r, $0; r = getline x; print r, "[" x "]" }
