Kind: optimization

`text.kern` tests byte strings for palindromes and a trailing byte, measures them without their trailing run of a byte, rotates them and reverses them in place, and prints three lines for each of two strings. Nearly every index into a string is bounds-checked at run time. Restructure the program so that the compiler's bounds-check analysis reports **no runtime-checked bounds checks** in the whole program (keep `main` and `report` as they are), without changing the output and without `unsafe`. Keep `reverse(s)`, `is_palindrome(s) -> bool`, `rotate(src, dst, k) -> usize`, `ends_with(s, b) -> bool` and `trim_len(s, b) -> usize` with their signatures and results, as their comments in the program describe them. The program must check cleanly and print:

```
true false 17
17 er odd or evennev
neve ro ddo reven
false true 11
8 lo, kern
..nrek ,olleh
```
