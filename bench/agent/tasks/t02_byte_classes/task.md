Kind: optimization

`classes.kern` classifies bytes through a 256-entry table and weights the classes through a 4-entry table, and prints five figures for each of three texts. Every table lookup is bounds-checked at run time. Restructure the program so that the compiler's bounds-check analysis reports **at most 2 runtime-checked bounds checks** in the whole program (the two slices in `main` count; keep `main` as it is), without changing the output and without `unsafe`. Keep `fill_classes(tab)`, `class_of(tab, b)` and `weight_of(w, c)` with their signatures, and keep `score`, `count_class` and `longest_run` looking bytes and classes up through them as they do now. The program must check cleanly and print:

```
4360
16
2
5
0
2674
7
6
4
3
0
0
0
0
0
```
