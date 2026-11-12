BEGIN { RS = "a" } { print NR ": [" $0 "]" }
