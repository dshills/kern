BEGIN { RS = ""; FS = "\t" } { print NF; print $1 "|" $2 "|" $3 }
