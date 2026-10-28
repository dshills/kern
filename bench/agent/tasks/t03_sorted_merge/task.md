Kind: implementation

Implement `merge` and `common` in `merge.kern`, keeping their signatures: `merge` writes the elements of two sorted views into `dst` in non-decreasing order, stops when `dst` is full, and returns the number written; `common` counts the values that occur in both sorted views, each matching pair counted once. The merge is on a hot path: the compiler's bounds-check analysis must report **at most 6 runtime-checked bounds checks** in the whole program (the six slices in `main` count; keep `main` as it is), and `is_sorted` must stay a function with its signature and result. No `unsafe`. The program must check cleanly and print:

```
11
1 2 3 3 5 6 7 7 8 9 11 
false
4
1 2 3 3 
2
2
true
0
4
1 3 5 7 
true
```
