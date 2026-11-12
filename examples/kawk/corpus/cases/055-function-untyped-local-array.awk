function f(n,   tmp, i) { for (i = 0; i < n; i++) tmp[i] = i; return length(tmp) }
function g(a) { a["k"] = 1; return 1 }
function h(b) { return g(b) }
BEGIN { print f(3), f(5); h(arr); print arr["k"], length(arr); print f(1) }
