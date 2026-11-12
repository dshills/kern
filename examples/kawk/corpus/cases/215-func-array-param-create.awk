function init(a) { a["k"] = "v" } function deep(b) { init(b) } function count(c,  n, k) { for (k in c) n++; return n + 0 }
BEGIN { init(x); print x["k"]; deep(y); print y["k"], count(y), count(z), length(z) }
