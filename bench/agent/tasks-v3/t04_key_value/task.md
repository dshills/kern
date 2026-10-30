Kind: optimization

`kv.kern` keeps a sorted key table beside a parallel value table and answers lookups, a range sum, a maximum and a clamped positional read, printing eight lines for each of three table pairs. Its indexes into the tables are bounds-checked at run time. Restructure the program so that the compiler's bounds-check analysis reports **no runtime-checked bounds checks** in the whole program (`main` has none; keep `main` as it is), without changing the output and without `unsafe`. `find` must remain a binary search over `keys`; keep `find`, `sum_range`, `max_value` and `key_of` with their signatures and results. The program must check cleanly and print:

```
33
500
900
none
1298
1200
20
90
none
500
none
none
1200
1200
20
90
none
none
none
none
0
none
none
none
```
