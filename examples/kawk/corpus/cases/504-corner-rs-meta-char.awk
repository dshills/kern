BEGIN { RS = "." } { gsub(/\n/, " "); print NR ": [" $0 "]" }
