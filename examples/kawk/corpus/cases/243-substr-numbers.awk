BEGIN { print substr(12345, 2, 3), substr(3.14159, 1, 4), substr(1e6, 1, 3), substr("", 1, 5) "|", substr(100 / 3, 1, 6) }
