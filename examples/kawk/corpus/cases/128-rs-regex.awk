BEGIN { RS = "[0-9]+|\n" } { print NR ": [" $0 "]" }
