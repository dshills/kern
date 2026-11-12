BEGIN { RS = ""; FS = ":" } { print NR, NF; for (i = 1; i <= NF; i++) printf "<%s>", $i; print "" }
