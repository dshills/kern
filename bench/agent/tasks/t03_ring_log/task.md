Kind: implementation

Implement `push`, `get`, `last`, `sum_newest`, `copy_out` and `max_window` in `ring.kern`, keeping their signatures. The file keeps a fixed-capacity log of `u64` samples in a ring: the entries live in `buf`, whose length is the capacity (never 0); `log.next` is the slot the next push writes and `log.count` the number of entries held. `push(log: edit[Log], buf: edit[]u64, x: u64)` appends `x` as the newest entry, overwriting the oldest one when the log is full; `get(log: view[Log], buf: view[]u64, i: usize) -> u64` is the entry at position `i` counted from the oldest (0 is the oldest), or 0 when `i` is not below `log.count`; `last(log: view[Log], buf: view[]u64) -> u64` is the newest entry, or 0 when the log is empty; `sum_newest(log: view[Log], buf: view[]u64, k: usize) -> u64` is the sum of the newest `k` entries, or of all of them when the log holds fewer than `k`; `copy_out(log: view[Log], buf: view[]u64, dst: edit[]u64) -> usize` writes the entries, oldest first, to the front of `dst` (which is at least as long as `buf`) and returns how many it wrote; `max_window(xs: view[]u64, w: usize) -> u64` is the largest sum of `w` consecutive values of `xs`, or 0 when `w` is 0 or longer than `xs`. The log sits on a hot path: the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program; keep `main`, `line` and `show` as they are. No `unsafe`. The program must check cleanly and print:

```
5
2
0
2
10
15
3
5 8 2 0 
13
2
9
9
17
4
2 7 1 9 
17
0
6
3
9
3
4 6 3 
6
```
