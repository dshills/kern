function fact(n) { return n <= 1 ? 1 : n * fact(n - 1) }
function fib(n) { return n < 2 ? n : fib(n - 1) + fib(n - 2) }
function gcd(a, b) { return b == 0 ? a : gcd(b, a % b) }
function hanoi(n, from, to, via) {
    if (n == 0) return 0
    return hanoi(n - 1, from, via, to) + 1 + hanoi(n - 1, via, to, from)
}
function fill(arr, n,   i) { for (i = 1; i <= n; i++) arr[i] = i * i }
function sum(arr,   k, s) { for (k in arr) s += arr[k]; return s }
function reverse(s,   i, r) { for (i = length(s); i > 0; i--) r = r substr(s, i, 1); return r }
function twice(x) { x = x x; return x }
BEGIN {
    print fact(10), fact(20), fib(12), gcd(1071, 462), hanoi(6, "a", "c", "b")
    fill(squares, 10); print sum(squares), length(squares)
    i = "kept"; s = "abc"; print reverse("kern awk"), twice(s), s, i
}
