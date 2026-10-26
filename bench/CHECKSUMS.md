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
