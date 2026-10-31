# Quiet-host checks, evening of 2026-10-31

**Host:** Apple M4 Pro (14 cores), macOS 27.2, clang 23.1.4, rustc
1.99.0, go 1.27.3. The owner cleared the machine for the night. Every
timing ran alone, with the harness's load gate at 3.5. **Compiler:**
55082d2.

## 1. Full harness run (`bench/REPORT.md`, 18:00)

Ten interleaved rounds, each started at a one-minute load of 3.08 to
3.43.

- **Throughput:** nine workloads meet the target. Four are undecided:

  | workload | ratio [95%] |
  |---|---|
  | btree | 0.90 [0.86, 0.94] |
  | arena_ast | 1.01 [0.89, 1.16] |
  | packet_parser | 0.90 [0.89, 0.92] |
  | lock_free_queue | 0.89 [0.84, 0.95] |

  thread_pool, undecided in the run of 11:03, meets at 0.96
  [0.93, 0.98].
- **Memory:** twelve meet, and btree misses at 1.23 (ADR-014).

## 2. The undecided rows, with more rounds

The undecided rows were run again with more rounds, at a one-minute
load of 2.22 to 3.01. The harness's output is
`quiet-host-2026-10-31-rows.md` and `quiet-host-2026-10-31-btree.md`.

| workload | rounds | ratio [95%] | verdict |
|---|---|---|---|
| arena_ast | 30 | 1.10 [1.07, 1.12] | meets |
| packet_parser | 30 | 0.91 [0.91, 0.91] | meets |
| lock_free_queue | 30 | 0.92 [0.91, 0.93] | meets |
| btree | 30 | 0.91 [0.89, 0.94] | undecided |
| btree | 60 | 0.93 [0.92, 0.94] | meets |

**All thirteen workloads meet the throughput target.** The memory target
is met by twelve; btree's 1.23 is the width of its checked handles.

## 3. Tail latency (the same run)

The harness samples one operation in N/4096, timed with the host's
monotonic clock, whose resolution is about 42 ns. kern's p99 is within
the interval of the better of C and Rust on every workload. The one
reading above the best is buffer_pool: 346 ± 20 ns against C's
342 ± 12 ns, a gap below the clock's resolution. Most single-threaded
rows sit at the clock's floor (42 ns) in all three languages.

| workload | p99: C | Rust | kern |
|---|---|---|---|
| hash_table | 125 ns | 125 ns | 125 ns |
| btree | 408 ns | 396 ns | 388 ns |
| wal | 125 ns | 125 ns | 117 ns |
| buffer_pool | 342 ns | 346 ns | 346 ns |
| thread_pool | 19.4 µs | 14.7 µs | 7.6 µs |
| lock_protected_map | 19.8 µs | 23.9 µs | 2.5 µs |

The two multi-threaded rows with real tails are where kern's
runtime-owned parking lock and condition variables
(`docs/reports/perf-gaps.md`) differ from pthread's: their p99 is a third
and a tenth of C's.

## 4. The owed inliner-sizing check

`docs/reports/inliner-sizing.md` left a back-to-back timing of
page_frame_allocator and intrusive_run_queue against a 0193abc build. On
the busy host both had read 1–2% low.

Measured here with 15 interleaved runs each, median timed `ns`, HEAD
against 0193abc:
- page_frame_allocator: 63.12 → 61.67 ms (×1.024);
- intrusive_run_queue: 142.14 → 140.88 ms (×1.009).

No regression.

## 5. Wall clock of the day's compile-time and interpreter changes

**Native builds**, runtime cache on against off
(`docs/reports/build-time.md`), median of 9 interleaved builds:
- btree release: 270 → 210 ms;
- arena_ast debug: 220 → 173 ms.

That is the 20% measured on the busy host, as instruction counts said.

**Interpreter** (`kern run --opt=release`), ac0e893 (before the day's
interpreter work) against HEAD, median timed `ns`:

| program | ac0e893 | HEAD | speedup |
|---|---|---|---|
| arena_ast | 6.74 s | 2.22 s | 3.0x |
| vector | 20.65 s | 11.90 s | 1.7x |
| hash_table | 53.09 s | 30.15 s | 1.8x |

**Proof pass**, `go test -bench BenchmarkProve -count 5`:
- `BenchmarkProveLexer`: 381–383 µs, against a gate of 0.5 ms;
- `BenchmarkProveSequentialGuards`: 855–873 µs, against a gate of
  1.0 ms.

Both gates are met in wall time.

## 6. ThreadSanitizer

Earlier full-suite runs on the busy host skipped the nine TSan fixtures
("ThreadSanitizer is not supported by this toolchain"). The harness's
probe compiles and runs a TSan program, and on a loaded host that
program failed transiently. Here the probe passes. The nine concurrency
fixtures, `race_raw_ptr` among them, passed in three runs out of three,
with no symbolizer hang.
