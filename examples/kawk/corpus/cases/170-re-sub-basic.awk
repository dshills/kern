{ sub(/a/, "A"); print } END { s = "hello"; print sub(/l+/, "L", s), s; print sub(/z/, "Z", s), s; t = "aaa"; sub(/a/, "b", t); print t; sub(/$/, "!", t); print t; sub(/^/, ">", t); print t }
