Kind: implementation

Implement `decode` and its three helpers `rd16`, `rd32` and `byte_sum` in `tlv.kern`, keeping the helpers' signatures and reading every payload through them. The buffer holds type-length-value records: one byte of type, one byte of length, then that many payload bytes. Type 1 carries a 16-bit big-endian counter, type 2 a 32-bit big-endian flag word, type 3 raw bytes; any other type is counted as skipped. `decode` returns the summary (record count, sum of the counters, OR of the flag words, sum of the raw bytes, skipped count) or `None` when a record's payload runs past the end of the buffer or a type-1 or type-2 payload has the wrong length. An empty buffer decodes to an all-zero summary.

The decoder is on a hot path: the compiler's bounds-check analysis must report **at most 5 runtime-checked bounds checks** in the whole program (the four slices in `main` count; keep `main` as it is). No `unsafe`. The program must check cleanly and print:

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
