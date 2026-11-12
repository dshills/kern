function gcd(a, b) { return b == 0 ? a : gcd(b, a % b) } function isprime(n,  i) { if (n < 2) return 0; for (i = 2; i * i <= n; i++) if (n % i == 0) return 0; return 1 }
BEGIN { print gcd(1071, 462), gcd(17, 5); for (i = 1; i <= 50; i++) if (isprime(i)) printf "%d ", i; print "" }
