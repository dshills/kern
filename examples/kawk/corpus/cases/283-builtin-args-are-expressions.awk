BEGIN { s = "hello world"; print substr(s, index(s, " ") + 1), length(substr(s, 1, 5) "x"), toupper(substr(s, 1, 1)) substr(s, 2), int(sqrt(length(s) * 10)), sprintf("%d", length(s)) + 1 }
