BEGIN { FS = ","; OFS = " | " } { $1 = $1; printf "| %s |\n", $0 } NR == 1 { line = $0; gsub(/[^|]/, "-", line); print "|-" line "-|" }
