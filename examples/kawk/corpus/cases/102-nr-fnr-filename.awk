FNR == 1 { print FILENAME == "" ? "stdin" : "file", NR, FNR } END { print NR, FNR }
