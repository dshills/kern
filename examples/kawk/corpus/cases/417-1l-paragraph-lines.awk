BEGIN { RS = ""; FS = "\n" } { print NR ": " NF " lines, first: " $1 }
