BEGIN { FS = "\t" } { print NF "<" $1 ">" "<" $2 ">" }
