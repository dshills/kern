# Quiet-host harness run, 2026-10-28

`go run ./bench/harness -experiment` at f2b3911, gated on no other harness
or `kern build` running and five consecutive 30-second samples of the
one-minute load below 3.5. The run took 07:08-07:09; load 1.7 to 2.2.
`bench/REPORT.md` is that run as measured. It is the first full run since
`bc20fda` (2026-10-27), so it also covers the union layout, inliner sizing,
`load_be`/`load_le`, `Vec.extend`, zeroed `Vec.resize`, release handles and
Pool slot layout changes.

## Throughput target, kern release against the best of C and Rust

| workload | before (REPORT at bc20fda era) | now | target |
|---|---|---|---|
| vector | 1.12 | 1.04 | meets |
| hash_table | 0.86 | 0.90 | meets (at the line) |
| btree | 0.84 | 0.96 | meets; RSS 1.44 → 1.23, still misses |
| arena_ast | 0.62 | 0.90 | meets; RSS 1.37 → 0.99 |
| ring_buffer | 1.03 | 1.02 | meets |
| wal | 0.63 | 0.99 | meets |
| packet_parser | 0.71 | 0.91 | meets |
| buffer_pool | 0.98 | 0.96 | meets |
| page_frame_allocator | 0.96 | 0.96 | meets |
| intrusive_run_queue | 1.01 | 1.02 | meets |
| thread_pool | 0.79 | 0.59 | misses |
| lock_protected_map | 0.62 | 0.57 | misses |
| lock_free_queue | 0.77 | 0.93 | meets |

Eleven of thirteen workloads now meet the throughput target, up from five.
Every checksum agrees across languages.

## The two multi-threaded misses

The kern rows did not regress. Timed back to back (seven runs each,
interleaved, load about 3) the binaries from `main` at 25dc715 and from
this branch agree: thread_pool 20.5 against 20.5 Mops/s at the median,
lock_protected_map 21.4 against 23.7. The C rows rose on the quiet host
(thread_pool 28.9 → 34.1, lock_protected_map 26.3 → 37.6). The earlier
report's ratios were flattered by C running on a loaded host.

Much of the gap is the mutex kind. `kern_mutex_new` creates a
`PTHREAD_MUTEX_ERRORCHECK` mutex, so recursive acquisition traps
(`kern_mutex_lock` turns `EDEADLK` into a trap). The C versions use a
default mutex. Linking the same kern LLVM with a runtime whose mutexes are
`PTHREAD_MUTEX_DEFAULT` (five runs, interleaved):

| | C | kern as built | default mutex |
|---|---|---|---|
| thread_pool, Mops/s | 33.2-34.2 | 20.1-22.3 (0.60) | 25.0-25.6 (0.75) |
| lock_protected_map, Mops/s | 33.3-38.0 | 23.8-24.4 (0.65) | 31.1-33.5 (0.86) |

A default mutex alone would deadlock where kern now traps. A fix would
keep the trap with cheaper bookkeeping, for example the owning thread
held in an atomic beside a default mutex and checked before locking. The
rest of thread_pool's gap (0.75) is not diagnosed.
