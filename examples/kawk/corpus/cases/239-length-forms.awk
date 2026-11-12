{ print length, length(), length($0), length($1), length($1 $2) } END { print length("hello"), length(12345), length(1 / 3), length(""), length(x), length("a\tb"), length 1 }
