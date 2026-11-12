BEGIN { RS = "" } { print NR ": [" $0 "]", NF; for (i = 1; i <= NF; i++) printf "<%s>", $i; print "" }
