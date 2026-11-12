NF > 2 { print substr($0, index($0, $2), length($2)), toupper(substr($1, 1, 1)) tolower(substr($1, 2)), length($NF) > 3 ? "long" : "short" }
