{ if ($1) t++; else f++; if ($0) tt++; else ff++ } END { print t, f, tt, ff }
