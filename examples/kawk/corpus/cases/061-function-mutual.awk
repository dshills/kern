function even(n) { return n == 0 ? 1 : odd(n - 1) }
function odd(n) { return n == 0 ? 0 : even(n - 1) }
BEGIN { print even(10), odd(7), even(7) }
