{ for (i = 1; i <= NF; i++) cell[NR, i] = $i; if (NF > w) w = NF } END { for (i = 1; i <= w; i++) { line = ""; for (r = 1; r <= NR; r++) line = line (r > 1 ? " " : "") cell[r, i]; print line } }
