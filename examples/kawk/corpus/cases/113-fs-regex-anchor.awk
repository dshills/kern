BEGIN { FS = "^a|b" } { print NF "|" $1 "|" $2 "|" $3 }
