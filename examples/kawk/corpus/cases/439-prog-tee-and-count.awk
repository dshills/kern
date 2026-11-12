{ print > "copy.txt"; n++ } END { close("copy.txt"); while ((getline l < "copy.txt") > 0) m++; print n, m, n == m }
