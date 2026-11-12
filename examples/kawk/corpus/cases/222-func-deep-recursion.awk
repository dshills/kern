function depth(n) { return n == 0 ? 0 : 1 + depth(n - 1) } function sum(n) { if (n == 0) return 0; return n + sum(n - 1) } BEGIN { print depth(500), sum(800) }
