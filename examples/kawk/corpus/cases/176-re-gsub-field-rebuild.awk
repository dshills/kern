BEGIN { OFS = "-" } NR <= 3 { gsub(/a/, "A", $1); print; gsub(/b/, "B"); print }
