BEGIN { FS = ":" } { print NR, NF, length($1); if (NF > 1) print "<" $1 "><" $2 "><" $NF ">" }
