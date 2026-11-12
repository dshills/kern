{ for (i = 1; i <= NF; i++) count[$i]++ } END { for (w in count) print w, count[w] }
