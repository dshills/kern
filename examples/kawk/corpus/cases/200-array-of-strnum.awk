{ seen[$1]++ } END { print seen["dup"], seen[10], seen["10"], seen[1], seen["1"], seen["3.14"], seen[3.14], ("" in seen), seen[""] }
