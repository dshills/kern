BEGIN { RS = "" } { n++ } END { print n + 0, NR }
