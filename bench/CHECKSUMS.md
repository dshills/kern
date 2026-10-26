# Benchmark work and checksum definitions

Every implementation (`bench/c`, `bench/rust`, `bench/kern`) of a workload
must do exactly the work below and print the same `checksum`, so the harness
can verify agreement. Everything is deterministic: no randomness other than
the shared LCG, no dependence on addresses or timing.

## Shared definitions

- **LCG.** `x = x * 6364136223846793005 + 1442695040888963407` on u64 with
  wrapping arithmetic, seed `42`. `next()` advances once and returns the new
  `x` (the first value returned is `42*a + c`, never the seed itself).
- **Wrapping.** "wrapping" means arithmetic modulo 2^64 (kern:
  `wrapping_add`/`wrapping_mul`). Where nothing is said, the value provably
  fits and plain arithmetic is used.
- **Protocol line.** `ops=<N> ns=<elapsed> p50=<ns> p99=<ns> checksum=<u64>`.
  `ns` covers the timed section only. Latency is sampled on operation `i`
  when `i % (ops / 4096) == 0` and fewer than 4096 samples were recorded, by
  reading the clock immediately before and after that single operation.
  `p50 = sorted[n/2]`, `p99 = sorted[n*99/100]` (integer division, n = number
  of samples). C uses `CLOCK_MONOTONIC_RAW` on macOS (`CLOCK_MONOTONIC` there
  has microsecond resolution) and `CLOCK_MONOTONIC` elsewhere; Rust uses
  `Instant`; kern uses `now_ns()`.
- **ops.** For workloads of the form "A pass then B pass" the op count is
  the number of A operations, the sampled operation is one A operation, and
  the B pass is inside the timed section but not counted as ops.
- Any check failure (missing key, CRC mismatch, malformed packet, wrong
  count) aborts with a non-zero exit instead of printing a line.

## vector (ops = 20,000,000)

Work: 4 rounds. Each round clears the vector (capacity retained; C starts at
capacity 8 and doubles, Rust `Vec` and kern `Vec` use their own growth, which
does not affect the checksum), pushes `PER = 5,000,000` values, then sums
all elements by iterating a view of the vector. The value pushed for global
push index `g = r*PER + k` (round `r`, position `k`) is `g * 3`.

Checksum: wrapping sum of the four round sums.
Expected: `599999970000000`.

## hash_table (ops = 5,000,000)

Work: open-addressing table, linear probing, structure of arrays
`states: u8` (0 empty, 2 full), `keys: u64`, `vals: u64`; initial capacity
16 (`bits = 4`); hash `h = (key * 0x9E3779B97F4A7C15 wrapping) >> (64 - bits)`;
probe `i = (i + 1) & (cap - 1)` while the slot is full with a different key.
`insert(key, val)`: first, if `count * 4 >= cap * 3`, grow: build a fresh
table with `bits + 1` and insert every full slot in index order, then replace;
then probe; a full slot with the same key overwrites the value, otherwise the
slot is filled and `count` increments. Phase 1: for `i` in `0..N`,
`insert(lcg.next(), i)`. Phase 2: re-seed the LCG, and for each `i` look up
`lcg.next()` (abort when absent).

Checksum: wrapping sum over the lookups of `(value XOR key)`, then plus
`count` (= N).
Expected: `2076684288481931776`.

## btree (ops = 2,000,000)

Work: order-16 B-tree (15 keys, 16 children, min degree `T = 8`) over u64
keys with u64 values; nodes live in a growable pool addressed by u32 index;
the root is pool index 0 (a leaf) at the start. Insert follows CLRS
(preemptive splitting): if the root has 15 keys, allocate a new non-leaf root
whose child 0 is the old root and split child 0; then descend: in node `n`
find `i` = number of keys less than `key` (scan from the right); an equal key
overwrites the value; in a leaf shift keys/values right and insert at `i`;
in an internal node, if child `i` has 15 keys, split it (the new right node
gets keys/values 8..14 and children 8..15, both halves keep 7 keys, the
median key/value index 7 is inserted into the parent at `i` with the new node
as child `i+1`), then if `key` equals the promoted key overwrite, else step
to child `i` (`i+1` when `key` is greater). The pool allocates the right node
of every split, so the node count is a function of the algorithm. For `i` in
`0..N`: `insert(lcg.next(), i)` (the keys are distinct). Then an in-order
traversal visits every key in ascending order with position `p = 0, 1, ...`.

Checksum: wrapping sum over the traversal of `(key XOR p) + value` (wrapping),
then XOR the number of pool nodes.
Expected: `13433169380067381159`.

## arena_ast (ops = 1,000,000)

Work: an arena of exactly N nodes addressed by u32 index: slot 0 is
`Num(0)`, a sentinel never referenced by an edge (the null handle); slots
1..999,999 are a balanced binary tree built post-order by `build(n)` with
`n` odd:

- `n == 1`: allocate `Num(lcg.next() >> 40)`.
- else `m = n - 1`, `l = m / 2`, `l -= 1` if `l` is even, `r = m - l`;
  `left = build(l)`, `right = build(r)`, `x = lcg.next()`; allocate
  `Add(left, right)` if `x >> 63 == 1`, else `Mul(left, right)`.

`build(N - 1)` is called once after the sentinel. One LCG draw per node, in
allocation order. The ops are the N allocations (the sentinel is op 0).
Evaluation is a recursive walk modulo the prime `M = 4294967291` (2^32 - 5):
`Num(v) -> v`; `Add -> (eval(l) + eval(r)) % M`; `Mul -> (eval(l) * eval(r)) % M`.
Operands are below 2^32, so no intermediate exceeds 2^64.

Checksum: `eval(root) + N`.
Expected: `3969867084`.

## ring_buffer (ops = 50,000,000)

Work: a ring of 1024 u64 slots stored inline, with `head`, `tail`, `len`;
indices advance with `& 1023`; `push` fails (hands the value back) when
`len == 1024`, `pop` fails when `len == 0`. Untimed prefill: push the values
`0..511`. Timed: for `i` in `0..N`: `push(512 + i)` then `pop()` → `p`
(which equals `i`); both must succeed. The ring holds 512 values at the end.

Checksum: starting from 0, for each popped `p`:
`cs = (cs XOR p) * 0x100000001b3` (wrapping).
Expected: `18130502550475885824`.

## wal (ops = 2,000,000)

Work: an in-memory log in a growable byte buffer. Record = 16-byte header +
64-byte payload. Header, little-endian: `lsn: u64`, `len: u32` (64),
`crc: u32` = CRC-32/IEEE of the payload (polynomial 0xEDB88320, reflected,
init 0xFFFFFFFF, final XOR 0xFFFFFFFF, byte-at-a-time table; no hardware
CRC). Payload for record `i` (lsn = i, starting at 0): 8 little-endian u64
words, word `j` = `(i + 1) * 0x9E3779B97F4A7C15 + j` (wrapping). Appends
are the ops. Then replay: walk the buffer from offset 0 while 16 header bytes
remain; abort if the payload would run past the end or its CRC differs from
the header; chain `cs = (cs XOR ((lsn << 32) | crc)) * 0x100000001b3`
(wrapping) starting from 0; exactly N records must be replayed.

Checksum: `cs` after replay.
Expected: `2630590959368386693`.

## packet_parser (ops = 10,000,000)

Work: untimed setup generates `P = 4096` packets back to back into one byte
buffer and records each packet's offset. Packet = 32-byte big-endian header
followed by `payload_len` bytes:

| offset | field | value |
|---|---|---|
| 0 | magic u32 | 0xCAFEF00D |
| 4 | version u8 | 1 |
| 5 | flags u8 | low 8 bits of `x3 >> 32` |
| 6 | hdr_len u16 | 32 |
| 8 | src u32 | `x1 >> 32` |
| 12 | dst u32 | low 32 bits of `x1` |
| 16 | src_port u16 | `x2 >> 48` |
| 18 | dst_port u16 | low 16 bits of `x2 >> 32` |
| 20 | seq u32 | low 32 bits of `x2` |
| 24 | payload_len u16 | `16 + (x3 >> 57)` (16..143) |
| 26 | csum u16 | Internet checksum of the header (see below) |
| 28 | ts u32 | low 32 bits of `x3` |

`x1, x2, x3` are three consecutive LCG draws per packet, in packet order.
Payload byte `j` = low 8 bits of `seq + j`. `sum16(bytes)` is the folded
ones'-complement sum of big-endian 16-bit words (an odd trailing byte
contributes `byte << 8`; fold while `s >> 16 != 0`: `s = (s & 0xFFFF) +
(s >> 16)`). `csum` = bitwise NOT of `sum16(header with csum = 0)`, so
`sum16(header) == 0xFFFF` for a valid header.

Timed: for `i` in `0..N`, take the packet at `offsets[i & 4095]`; parse: at
least 32 bytes must remain, `magic` and `hdr_len` must match, `sum16(header)`
must be 0xFFFF, and `32 + payload_len` bytes must remain; decode every field
into a header struct; then `psum = sum16(payload view)`.

Checksum: wrapping sum over the ops of `seq XOR (psum << 32)`.
Expected: `9459311143157445824`.

## Phase 3 workloads (PLAN.md 3.4)

Shared conventions for the six workloads below:

- **Failure-path runs.** Workloads with a failure path read the environment
  variable `KERN_BENCH_FAIL`; when it is exactly `1` they set up the failing
  situation described under the workload, count every failed operation in
  `failures`, give a failed operation no checksum contribution, and print
  the protocol line with the extra field ` failures=<N>`. `ops` stays the
  workload constant. In a normal run a failure aborts (non-zero exit).
- **Threads.** Multi-threaded workloads spawn their threads inside the timed
  section and join them before reading the clock. Per-op samples are taken on
  one thread only (named below) with that thread's own op counter `i`;
  `stride = ops / 4096` is computed from the workload's total `ops`. The
  checksum is a wrapping sum (or chain in a fixed order) over all completed
  ops, so it does not depend on scheduling.
- **Spin hint.** "spin" is one `pause` on x86-64 and one `isb` on AArch64
  (Rust's `std::hint::spin_loop`).
- **Expected values** were measured with the C and Rust programs, which
  agree on every value (normal and failure-path runs).

## buffer_pool (ops = 5,000,000)

Work: a backing store of `PAGES = 1024` pages of 4096 bytes; page `p` holds
`p` as a little-endian u64 in bytes 0..8, a little-endian u64 counter (0) in
bytes 8..16, and zeros elsewhere. A pool of `FRAMES = 256` frames, each
`{ data: [4096]u8, page: u64 (NONE = u64 max), last: u64 (0), pins: u32 (0),
dirty: bool (false) }`, a direct-mapped page table `table: [1024]u32`
(frame index or NONE), `tick: u64 = 0` and `evictions: u64 = 0`.

- `pin(p) -> Result[frame index, AllPinned]`: if `table[p] != NONE` the frame
  is a hit: `pins += 1`, return it. Otherwise choose the victim: scanning
  frames 0..255 in order, the frame with `pins == 0` and the smallest `last`
  (strictly smaller wins, so the lowest index wins ties; an empty frame has
  `last = 0` and is always preferred over a used one). No candidate ->
  `Err(AllPinned)` with nothing changed. If the victim holds a page:
  `table[old] = NONE`, copy its 4096 bytes to the store when `dirty`,
  `dirty = false`, `evictions += 1`. Then copy store page `p` into the frame,
  `page = p`, `pins = 1`, `table[p] = frame`.
- `unpin(f)`: `pins -= 1`, `tick += 1`, `last = tick`.

Timed: for `i` in `0..N`: `x = lcg.next()`; page `p = (x >> 32) & 127` when
`x >> 56 < 224` (87.5%: a hot set of 128 pages), else `(x >> 32) & 1023`.
`f = pin(p)`; abort unless bytes 0..8 of the frame equal `p`; add 1 to the
counter in bytes 8..16; `dirty = true`; `unpin(f)`; `cs += p | (f << 16)`
(wrapping; `f` is the frame index). Sampled operation: the pin, the
verification and counter update, and the unpin. Untimed after the loop:
copy every dirty frame that holds a page back to the store; then for `p` in
`0..1024`: `cs += counter(p) * (p + 1)` (wrapping); then `cs += evictions`.

Checksum: `cs`.
Expected: `41278722885634`.

Failure path (`KERN_BENCH_FAIL=1`): before the loop, `pin(p)` for `p` in
`0..256` (they land in frames 0..255 in order) and never unpin them. Every
miss then finds no victim: `failures += 1`, nothing else (hits on pages
0..255 take `pins` 1 -> 2 -> 1 and contribute as usual). The flush and the
final sums are unchanged.
Expected: `failures=467921`, checksum `19521225238341`.

## page_frame_allocator (ops = 10,000,000)

Work: a bitmap of `WORDS = 1024` u64 words over `NFRAMES = 65536` frames, set
bit = free, all set at start; `free = 65536`; `hint = 0`. A FIFO ring of at
most `MAX_HELD = 4096` held frame numbers (`held`, `head`, `len`).

- `alloc() -> Result[u32, OutOfFrames]`: `Err` if `free == 0`. Otherwise
  `w = hint`; up to 1024 times: `word = bits[w]`; if `word != 0`:
  `bit = trailing_zeros(word)`, `bits[w] = word & (word - 1)`, `free -= 1`,
  `hint = w`, return `w * 64 + bit`; else `w = (w + 1) & 1023`.
- `free(f) -> Result[(), FreeErr]`: `f >= 65536` is `OutOfRange`;
  `w = f / 64`, `m = 1 << (f % 64)`; a set bit is `DoubleFree`; else set the
  bit, `free += 1`, and `hint = w` if `w < hint`.

Timed: for `i` in `0..N`: `x = lcg.next()`; allocate when `len == 0`, or
when `x >> 63 == 0` and `len < 4096`; otherwise free the oldest held frame.
On `Ok(f)` from alloc: append `f` to the ring, `cs = (cs XOR f) *
0x100000001b3`. On a free of `f` (abort on `Err`): `cs = (cs XOR (f | (1 <<
32))) * 0x100000001b3`. Sampled operation: the alloc or the free. After the
loop `cs += free`.

Checksum: `cs`.
Expected: `12777962174013961231`.

Failure path: after the init, clear words `16..1023` (frames 1024..65535 are
allocated and never freed) and set `free = 1024`. An alloc that returns
`Err(OutOfFrames)` counts `failures += 1` and contributes nothing; the ring
is not touched.
Expected: `failures=5842`, checksum `9117231987536972120`.

## intrusive_run_queue (ops = 10,000,000)

Work: `TASKS = 1024` tasks stored inline in one array; task `i` has
`id = i`, `prio = i` and embedded `next`/`prev` links. The queue
(`head`, `tail`, `len`) is built by inserting tasks 0..1023 in order with the
insert below, so it starts non-decreasing in `prio` from head to tail.

- `pop()`: unlink and return the head (abort when empty).
- `insert(t)`: `p = tail`; while `p != null` and `p.prio > t.prio`:
  `p = p.prev`; link `t` after `p` (at the head when `p == null`), fixing
  `prev`/`next`/`tail`; `len += 1`.

Timed: for `i` in `0..N`: `x = lcg.next()`, `delta = 1024 + (x >> 58)`
(1024..1087, which keeps the tail scan short, about 10 nodes); `t = pop()`;
`t.prio += delta`; `insert(t)`; `cs += t.id XOR t.prio` (wrapping). Sampled
operation: pop, bump, insert. After the loop (`len` must be 1024) walk from
the head with position `k = 0, 1, ...`: `cs += (k + 1) * (t.id + 1)`
(wrapping).

Checksum: `cs`.
Expected: `51548582311292`.

## thread_pool (ops = 1,000,000)

Work: an input of `BLOCKS = 4096` blocks of `WORDS = 64` u64 words, word `k`
of the whole input = `k * 0x9E3779B97F4A7C15` (wrapping). Job `b` (a u64
job index) computes `sum` = wrapping sum of the 64 words of block
`b & 4095`, and yields `sum XOR b`. A queue state behind one mutex:
`ring: [2048]u64` of job indices (FIFO: `head`, `len`), `total: u64`,
`done_jobs: u64`, `done: bool`; two condition variables `ready` and `space`.

- Workers (4, spawned first): loop: lock; if a result is pending from the
  previous job, `total += result` (wrapping), `done_jobs += 1`; while
  `len == 0 && !done` wait on `ready`; if `len == 0` (done) unlock and exit;
  pop the head job; if `len == 1024` after the pop, signal `space`; unlock;
  run the job and keep its result pending. Worker 0 samples: its `i`-th
  iteration from before the lock to after the job's computation.
- Main thread: for `b = 0, 1024, 2048, ...` below N, a batch of
  `min(1024, N - b)` jobs (the last batch has 576): lock; while `len > 1024`
  wait on `space`; push jobs `b..b+n`; broadcast `ready`; unlock. Then lock,
  `done = true`, broadcast `ready`, unlock, join the workers.

Timed: spawning the workers through joining them. `done_jobs` must equal N.
Threads: 4 workers + the main thread.

Checksum: `total + N` (wrapping).
Expected: `14753395421204281632`.

## lock_protected_map (ops = 4,000,000)

Work: the hash_table map (same hash, probe, growth and layout as above)
behind one mutex. Untimed setup: `insert(k, k * 0x9E3779B97F4A7C15)` for `k`
in `0..K` with `K = 262144`. Four threads `t = 0..3`, each with its own LCG
seeded `42 + t` (seeds 42, 43, 44, 45), each doing `1,000,000` ops; op:
`x = lcg.next()`; if `x >> 62 == 0` (25%): `key = K + (x >> 46)`, lock,
`insert(key, key * 0x9E3779B97F4A7C15)`, unlock, `cs_t += key`; else
`key = x >> 46` (always present; abort if not), lock, `v = find(key)`,
unlock, `cs_t += v XOR key`. Thread 0 samples its `i`-th op (lock, the
operation, unlock). Timed: spawning the threads through joining them.
Threads: 4.

Checksum: `cs_0 + cs_1 + cs_2 + cs_3 + count` (wrapping; `count` is the
final number of keys).
Expected: `1619761243042050329`.

## lock_free_queue (ops = 8,000,000)

Work: a single-producer single-consumer ring `slots: [4096]u64` with two
monotonically increasing u64 atomics on separate cache lines, `head`
(consumer) and `tail` (producer), a `go` flag and an `expected` count. Each
side keeps a private copy of its own index (`pos`) and a cached copy of the
other side's published index (`cache`), both 0 at start, and publishes its
own index with a release store every `B = 64` elements and whenever it is
about to wait (B-Queue style batching; publishing on every element lets the
two threads fall into a lock-step regime, 20-40x slower on Apple silicon,
where every release store stalls on the line the other thread is polling).

- `push(v) -> bool`: if `pos - cache == 4096`: `cache = head.load(acquire)`;
  still equal -> return false. `slots[pos & 4095] = v`; `pos += 1`; if
  `pos % 64 == 0`: `tail.store(pos, release)`; return true.
- `pop() -> Option[u64]`: if `pos == cache`: `cache = tail.load(acquire)`;
  still equal -> None. `v = slots[pos & 4095]`; `pos += 1`; if `pos % 64 ==
  0`: `head.store(pos, release)`; return `v`.
- `publish_tail()` / `publish_head()`: store `pos` with release.

The main thread is the producer, the spawned thread the consumer. Consumer:
spin until `go` (acquire); `expected = N` (see the failure path); for `i` in
`0..expected`: until `pop` succeeds { `publish_head()`; spin }; `cs = (cs XOR
v) * 0x100000001b3` (wrapping, from 0); sampled operation: the `i`-th pop
including its wait. Then `publish_head()`. Producer: `go.store(true,
release)`; for `i` in `0..N`: `v = lcg.next()`; until `push(v)` succeeds {
`publish_tail()`; spin }; then `publish_tail()`; join. Timed: spawning the
consumer through joining it. Threads: 2.

Checksum: `cs + expected` (wrapping).
Expected: `9933137394116826624`.

Failure path: before `go` is set, the producer attempts `push(lcg.next())`
once for each of elements `0..5120`: the first 4096 succeed, the next 1024
return false (`failures = 1024`, those values are dropped); then
`publish_tail()`, `expected = N - failures` is stored before `go`, and the
producer continues with elements `5120..N` as in the normal run.
Expected: `failures=1024`, checksum `16524715534916428288`.
