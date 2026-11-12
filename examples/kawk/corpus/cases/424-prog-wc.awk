{ chars += length($0) + 1; words += NF } END { printf "%7d %7d %7d\n", NR, words, chars }
