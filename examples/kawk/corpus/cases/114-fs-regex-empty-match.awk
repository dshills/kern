BEGIN { FS = "x*" } { print NF "|" $1 "|" $2 }
