{ $0 = tolower($0); gsub(/[^a-z ]/, " "); for (i = 1; i <= NF; i++) freq[$i]++ }
END { for (w in freq) printf "%s\t%d\n", w, freq[w] | "sort -k2,2nr -k1,1"; close("sort -k2,2nr -k1,1") }
