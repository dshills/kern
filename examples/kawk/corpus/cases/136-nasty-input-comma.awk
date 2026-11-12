BEGIN { FS = "," } NR > 4 { print NR, NF; $2 = "Z"; if (length($0) < 100) print }
