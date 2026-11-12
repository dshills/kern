{ c[$0]++ } END { for (l in c) if (c[l] > 1) print c[l], l }
