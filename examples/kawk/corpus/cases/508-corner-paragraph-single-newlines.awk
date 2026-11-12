BEGIN { RS = "" } { print NR ": " NF " [" $0 "]" }
