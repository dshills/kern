function f() { return } function g() { } BEGIN { x = f(); y = g(); print "[" x "][" y "]", length(x), x + 0 }
