BEGIN { print length("a") length("bb"), length("ccc") + 1, length() + 0, length + 1, length "x", -length("ab"), !length(""), length("a" "b") }
