BEGIN { printf "%50s|\n%-50s|\n%050d|\n%.40f\n%60.50f\n", "right", "left", 42, 1 / 3, 2 / 3; printf "%.60g\n%.60e\n", 0.1, 1 / 3; printf "%300d|\n", 7 }
