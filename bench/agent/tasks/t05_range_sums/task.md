Kind: optimization

`ranges.kern` answers range queries over a series of samples through its prefix sums: it builds the prefix sums, sums ranges, finds the largest window sum, lists window sums and finds, by binary search, the shortest window from a given sample whose sum reaches a target; it prints five lines for each of two series. Nearly every index into a series or its prefix sums is bounds-checked at run time. Restructure the program so that the compiler's bounds-check analysis reports **no runtime-checked bounds checks** in the whole program (keep `main`, `put` and `report` as they are), without changing the output and without `unsafe`. Keep `prefix(xs, ps)`, `range_sum(ps, a, b) -> u64`, `best_window(xs, w) -> u64`, `window_sums(xs, ps, w, sums) -> usize` and `first_reaching(ps, a, target) -> usize` with their signatures and results, as their comments in the program describe them. The program must check cleanly and print:

```
0 3 4 8 9 14 23 25 31 
10 31 0 
17 9 27 
7 4 5 5 6 14 11 8 
5 3 0 
0 10 10 10 17 19 
9 19 0 
10 10 0 
3 10 0 7 
1 2 0 
```
