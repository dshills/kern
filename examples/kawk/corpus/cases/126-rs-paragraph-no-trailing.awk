BEGIN { RS = "" } { print NR ": [" $0 "]" } END { print NR }
