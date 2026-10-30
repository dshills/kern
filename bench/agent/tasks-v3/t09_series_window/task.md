Kind: optimization

`series.kern` smooths a series of samples, counts its peaks, finds its largest window sum and its longest rise, and prints six lines for each of three series. Nearly every index into a series is bounds-checked at run time. Restructure the program so that the compiler's bounds-check analysis reports **no runtime-checked bounds checks** in the whole program (`main` has none; keep `main` as it is), without changing the output and without `unsafe`. Keep `smooth`, `peaks`, `max_window` and `longest_rise` with their signatures and results. The program must check cleanly and print:

```
3 2 2 3 5 5 5 4 4 4 5 
3
17
9
0
3
3 1 
0
0
3
0
1

0
0
0
0
0
```
