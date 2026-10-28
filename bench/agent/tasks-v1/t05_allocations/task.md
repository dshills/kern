Kind: optimization

`grow.kern` prints the right answer but allocates in a loop: every iteration creates a fresh `Vec` and a `Box`. Rewrite it so the program has at most 2 allocation sites (as reported by the compiler's allocation analysis) and still prints:

```
4950
```
