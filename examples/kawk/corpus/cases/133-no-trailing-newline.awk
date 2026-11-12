{ print NR ": [" $0 "]" } END { print NR, "[" $0 "]" }
