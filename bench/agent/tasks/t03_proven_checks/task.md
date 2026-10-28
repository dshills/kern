Kind: optimization

`frames.kern` scans a stream of 6-byte frames and prints four totals. Every index into the stream is bounds-checked at run time because the way the loops and helpers are written gives the compiler nothing to prove them with. The compiler removes a bounds check when it can prove the index in bounds from the function's own control flow: a comparison of the index (or the index plus a constant) against `len(x)` of the same sequence that dominates the access, a constant index under a dominating length check, or a `for` loop over the sequence. Restructure the program so that its bounds-check analysis reports **at most 4 runtime-checked bounds checks** in the whole program (the three slices of `stream` in `main` count; keep `main` as it is), without changing the output, and without `unsafe`. The program must check cleanly and print:

```
1526
65535
2019
1526
```
