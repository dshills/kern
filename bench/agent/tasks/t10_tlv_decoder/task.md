Kind: implementation

Implement `decode` in `tlv.kern`. The buffer holds type-length-value records: one byte of type, one byte of length, then that many payload bytes. Type 1 carries a 16-bit big-endian counter, type 2 a 32-bit big-endian flag word, type 3 raw bytes; any other type is counted as skipped. `decode` returns the summary (record count, sum of the counters, OR of the flag words, sum of the raw bytes, skipped count) or `None` when a record's payload runs past the end of the buffer or a type-1 or type-2 payload has the wrong length. An empty buffer decodes to an all-zero summary.

The decoder is on a hot path: the compiler's bounds-check analysis must report **at most 5 runtime-checked bounds checks** in the whole program (the four slices in `main` count; keep `main` as it is). The compiler removes a bounds check when the function's own control flow proves the index in bounds: a comparison of the index (or the index plus a constant) against `len(x)` of the same sequence that dominates the access, a constant index under a dominating length check, or a `for` loop over the sequence. No `unsafe`. The program must check cleanly and print:

```
7
500
65799
60
1
0
0
0
0
0
malformed
malformed
```
