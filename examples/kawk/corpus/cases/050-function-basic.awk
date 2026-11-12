function add(a, b) { return a + b }
function hello(name) { print "hello, " name }
function noret() { x = 5 }
BEGIN { print add(1, 2); hello("world"); v = noret(); print v "|" x; print add(add(1, 2), add(3, 4)) }
