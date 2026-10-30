Kind: refactoring

`mat.kern` works on square matrices stored row by row in a flat sequence (entry (r, c) of an n x n matrix `m` is `m[r * n + c]`), with that index arithmetic written out inline in every function. Refactor it: add `at(m: view[]u64, n: usize, r: usize, c: usize) -> u64`, which returns entry (r, c) of `m`, and `put(m: edit[]u64, n: usize, r: usize, c: usize, v: u64)`, which sets entry (r, c) of `m` to `v`, and make `fill`, `flip`, `symmetrize`, `trace`, `row_sums`, `col_sums`, `row_reversed`, `col_reversed` and `anti_diagonal` do every read and write of a matrix entry through them, keeping those nine functions with their signatures and results. The output must not change. The matrix code runs in an inner loop, so the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program (keep `main`, `show` and `report` as they are). No `unsafe`. The program must check cleanly and print:

```
33
3 33 63 
30 33 36 
12 11 10 
21 11 1 
2 11 20 
2 1 0 12 11 10 22 21 20 
33
0 10 20 10 11 21 20 21 22 
33
30 42 63 
66
6 46 86 126 
60 64 68 72 
13 12 11 10 
31 21 11 1 
3 12 21 30 
3 2 1 0 13 12 11 10 23 22 21 20 33 32 31 30 
66
0 10 20 30 10 11 21 31 20 21 22 32 30 31 32 33 
66
60 73 95 126 
60 73 
```
