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

The harness (`bench/harness`, Go) builds every implementation, runs each row
five times, and reports in `bench/REPORT.md`: throughput (ops/s), p50/p99,
peak RSS (rusage), text-section bytes, compile time, unsafe source lines,
allocation and copy counts from `kern show-allocations`/`show-copies`, the
ownership graph size from `explain-ownership`, and whether the reserved
extension was used (always "no" in Phase 2), with 95% confidence intervals
over the five runs. kern rows exist for `--opt release` (primary) and
`--opt debug`; the user-level handle code and the library's internals are
reported as separate rows where the workload has both (§2.3(a)/(b)).
