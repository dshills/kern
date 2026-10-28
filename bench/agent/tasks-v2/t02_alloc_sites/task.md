Kind: optimization

`words.kern` prints word statistics for two texts. Rewrite it so that the compiler's allocation analysis reports **at most 1 allocation site** in the whole program, keeping the same output, the function `stats` and the `Stats` struct with its four fields, and no `unsafe`. The program must check cleanly and print:

```
9
5
11
102134282586790
11
9
20
82639991608527229
```
