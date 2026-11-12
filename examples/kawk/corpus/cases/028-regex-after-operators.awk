{ if ($0 ~ /fox/ || /dog/) n++ }
END { print n; print !/x/; x = /e/ ? "yes" : "no"; print x }
