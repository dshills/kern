# Benchmark protocol (PLAN.md 2.6)

Every workload has three implementations, `bench/c/<w>/`, `bench/rust/<w>/` and
`bench/kern/<w>/`, that do the same work on the same inputs with the same
operation count and print exactly one line on standard output:

```
ops=<N> ns=<total elapsed nanoseconds> p50=<ns per op> p99=<ns per op> checksum=<u64>
```

- `ops` is the number of timed operations (the per-workload constant in the
  table below). `ns` is the wall-clock time of the timed section only
  (monotonic clock; setup and teardown excluded). `p50`/`p99` are percentiles
  of per-operation latency measured in nanoseconds over the timed operations,
  sampled every `ops/4096` operations (so each program records at most 4096
  samples). `checksum` is a deterministic value derived from the final data
  structure so the harness can verify the three implementations agree.
- No arguments are read: each program hard-codes its workload constant.
- The program's exit code is 0 on success.
- C and Rust use `clock_gettime(CLOCK_MONOTONIC)` / `std::time::Instant`;
  kern uses `now_ns()`.

| Workload | ops | What is timed |
|---|---|---|
| vector | 20,000,000 | push of u64 then a sum pass over a view; 4 rounds of 5M pushes into a cleared vector |
| hash_table | 5,000,000 | open-addressing insert of (u64 key, u64 value), then lookup of every key; keys from a 64-bit LCG |
| btree | 2,000,000 | insert of u64 keys into an order-16 B-tree stored in a pool of nodes, then ordered traversal |
| arena_ast | 1,000,000 | build of a binary expression tree of 1M nodes in an arena, then evaluation |
| ring_buffer | 50,000,000 | push/pop of u64 through a 1024-slot ring buffer |
| wal | 2,000,000 | append of 64-byte records to an in-memory log with a running CRC, then replay |
| packet_parser | 10,000,000 | parse of a 32-byte header from a byte buffer into a header struct and a checksum over the payload view |

The harness (`bench/harness`, Go) builds every implementation, then measures
them in interleaved rounds (`-runs`, default 10, at least 5): each round runs
every row once, workload after workload, with a workload's rows in an order
rotated by one each round, so the rows compared with each other run seconds
apart in every round and a burst of load on the host lands on all of them
rather than on every run of one row. It reports in `bench/REPORT.md`:
throughput (ops/s), p50/p99, peak RSS (rusage), text-section bytes, compile
time, unsafe source lines, allocation and copy counts from
`kern show-allocations`/`show-copies`, the ownership graph size from
`explain-ownership`, and whether the reserved extension was used (always "no"
in Phase 2), with 95% confidence intervals over the rounds.

The targets compare kern's release row with its reference, the C or Rust
release row with the higher mean throughput (or the lower mean peak RSS;
C on a tie), round by round: each round in which both rows ran gives a
paired ratio, and the report gives the ratios' geometric mean and its 95%
interval (the mean of their logarithms ± Student's t at n−1 degrees of
freedom times the standard error, mapped back with exp). The throughput
target (at least 0.90 of the reference) is **met** when the interval's
lower bound is at least 0.90, **missed** when its upper bound is below 0.90,
and **undecided** otherwise; the memory target (at most 1.10) is met when
the upper bound is at most 1.10, missed when the lower bound is above 1.10,
undecided otherwise; fewer than two paired rounds is undecided. The
report's header gives the one-minute load at the start of every round as a
range. `-max-load X` makes the harness wait before each round until the
load is at most X (polling every 15 s, for at most `-load-wait`, default
30 minutes, after which the round starts and the report says so);
timings meant for a report are taken on a quiet host, with nothing else
benchmarking (`specs/bench-interleave/SPEC.md`). kern rows exist for `--opt release` (primary) and
`--opt debug`; the user-level handle code and the library's internals are
reported as separate rows where the workload has both (§2.3(a)/(b)).

## Phase 3 workloads (PLAN.md 3.4)

| Workload | ops | What is timed |
|---|---|---|
| buffer_pool | 5,000,000 | pin/unpin of 4 KiB pages in a 256-page pool with LRU eviction over a 1024-page backing store; op = one pin (with its possible eviction) followed by unpin; a page's first 8 bytes carry its page number and are verified on pin |
| page_frame_allocator | 10,000,000 | alloc/free of 4 KiB frames from a bitmap allocator over 65,536 frames; op = one alloc or free, driven by the LCG (alloc when the drawn bit is 0 or no frame is held, else free the oldest held frame; at most 4096 held) |
| intrusive_run_queue | 10,000,000 | an intrusive doubly linked run queue of 1024 tasks with priorities; op = pop the head, bump its priority by the LCG, re-insert in priority order (insertion scan from the tail) |
| thread_pool | 1,000,000 | 4 worker threads pulling fixed-size jobs (sum of a 64-word block) from a mutex-protected queue fed by the main thread in batches of 1024; op = one job; the result is accumulated under the queue's mutex |
| lock_protected_map | 4,000,000 | 4 threads performing insert/lookup on one open-addressing hash map behind a Mutex (1M ops each, LCG keys, 75% lookups); op = one map operation |
| lock_free_queue | 8,000,000 | a bounded single-producer single-consumer ring (4096 slots) with release/acquire indices; op = one element passed from the producer thread to the consumer thread |

Multi-threaded workloads print the same single line; `ns` is wall time of the
timed section on the main thread, and per-op samples are taken on the thread
that completes the op (one thread's samples are enough for p50/p99). The
checksum must be deterministic (independent of scheduling): sums over all
completed ops, never over which thread did what.

Failure-path runs: a program that accepts the environment variable
`KERN_BENCH_FAIL=1` exercises its failure path (buffer_pool: a pin when every
frame is pinned returns an error instead of evicting; page_frame_allocator: an
allocation with no free frame returns an error; lock_free_queue: a push on a
full ring returns false) and prints the normal line with an extra
` failures=<N>` field; bench/CHECKSUMS.md defines each. The harness runs those
three workloads once more with the variable set and reports the count in a
"failure path" column (n/a when the program prints no failures field). The
report also carries a "threads" column (1 for single-threaded workloads;
thread_pool 4+1, lock_protected_map 4, lock_free_queue 2). Every `*.kern` file
in `bench/kern/<w>/` is a separate row named by its file stem (for example
`packet_parser` and `packet_parser_simd`), each built with `--opt=release` and
`--opt=debug`.
