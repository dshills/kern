Kind: optimization

`frames.kern` scans a stream of 6-byte frames and prints four totals. Every index into the stream is bounds-checked at run time because the way the loops and helpers are written gives the compiler nothing to prove them with. Restructure the program so that the compiler's bounds-check analysis reports **at most 4 runtime-checked bounds checks** in the whole program (the two slices of `stream` in `main` count; keep `main` as it is), without changing the output and without `unsafe`. Keep the helpers `rd16(buf, at)` and `frame_ok(buf, at)` with their signatures, and keep `total_values`, `largest_tag` and `byte_sum` calling them as they do now. The program must check cleanly and print:

```
1526
65535
2019
1526
```
