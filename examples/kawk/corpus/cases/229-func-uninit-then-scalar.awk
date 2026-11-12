function f(x) { x = 5; return x } function g(x) { return x "" } BEGIN { print f(u), "[" u "]", g(v), length(v); u = 3; print f(u), u }
