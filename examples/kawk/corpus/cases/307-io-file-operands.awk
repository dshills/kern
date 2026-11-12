FNR == 1 { files++ } { lines++ } END { print files, lines, FNR }
