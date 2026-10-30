Kind: implementation

Implement `at`, `transpose`, `mul` and `trace` in `mat.kern`, keeping their signatures: square matrices of `u32` are stored row-major in flat views, so the element at row `r` and column `c` of an n-by-n matrix is at index `r * n + c`. `at` returns an element, or 0 outside the matrix; `transpose`, `mul` and `trace` read every element through `at`. The matrix code is on a hot path: the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program (`main` has none; keep `main` as it is). No `unsafe`. The program must check cleanly and print:

```
1 4 7 2 5 8 3 6 9 
15
30 24 18 84 69 54 138 114 90 
189
9
0
0
1 3 2 4 
7 10 15 22 
5
```
