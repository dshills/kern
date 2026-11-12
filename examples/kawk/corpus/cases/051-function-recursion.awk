function fact(n) { return n <= 1 ? 1 : n * fact(n - 1) }
function fib(n) { if (n < 2) return n; return fib(n - 1) + fib(n - 2) }
function ack(m, n) { if (m == 0) return n + 1; if (n == 0) return ack(m - 1, 1); return ack(m - 1, ack(m, n - 1)) }
BEGIN { print fact(10), fact(20), fib(20), ack(2, 3) }
