Kind: implementation

Implement `histogram`, `digit_counts`, `most_frequent`, `top_digit`, `longest_run` and `final_run` in `stats.kern`, keeping their signatures: `histogram(data: view[]u8, counts: edit[[256]u64])` adds one to `counts[b]` for every byte `b` of `data`; `digit_counts(data: view[]u8, digits: edit[[10]u64])` adds one to `digits[d]` for every ASCII digit of `data` whose value is `d` (the digit `'0'` is the byte 48) and ignores other bytes; `most_frequent(counts: view[[256]u64]) -> u8` is the byte with the largest count and `top_digit(digits: view[[10]u64]) -> u8` the digit with the largest count, each the smallest such on a tie (so 0 when every count is 0); `longest_run(data: view[]u8) -> u64` is the length of the longest run of one repeated byte, and `final_run(data: view[]u8) -> u64` the length of the run that ends `data` (how many bytes at its end are equal to its last byte), each 0 for empty data. The statistics run over large inputs: the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program; keep `main` and `line` as they are. No `unsafe`. The program must check cleanly and print:

```
4
4
32
5
3
2
2
2
122
2
3
1
0
0
0
0
```
