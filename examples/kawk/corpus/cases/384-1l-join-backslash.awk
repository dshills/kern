/\\$/ { sub(/\\$/, ""); getline t; print $0 t; next }; 1
