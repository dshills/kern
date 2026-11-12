{ line = $0; gsub(/[^a-zA-Z ]/, "", line); line = toupper(line); n = split(line, w); if (n > 3) print n, w[1], w[n] }
