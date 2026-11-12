BEGIN { for (i = 0; i < 4; i++) { for (j = 0; j < 4; j++) { if (j == 2) break; if (i == 1) continue; printf "%d.%d ", i, j } } print "" }
