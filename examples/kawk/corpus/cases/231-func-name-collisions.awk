function length2(s) { return length(s) * 2 } function substring(s) { return substr(s, 2) } BEGIN { print length2("abc"), substring("hello"), length("xyz") }
