BEGIN { s = "a b"; gsub(/ /, "\\\\ ", s); print s; s = "x"; gsub(/x/, "a\\tb", s); print s; s = "x"; gsub(/x/, "$1 \\1 %s", s); print s; s = "path/to"; gsub("/", "\\/", s); print s }
