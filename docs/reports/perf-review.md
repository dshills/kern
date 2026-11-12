# A performance review of the language, the compiler and the runtimes

**Date:** 2026-11-12. **At commit:** `c0634b6`. **Status:** a review; each
item says what it would change, what it would gain and how that is known.
The first of its quick items were implemented the same day and are listed
under "What was done" at the end, with the numbers measured after; the
rest is open. One compiler defect it found is in section 7, with two
defects of measurement.

## The question

Where is there performance still to be had: in the code kern produces, in
the toolchain's own speed, in the interpreter, in the two runtimes, in the
library, and in what the language makes a program pay?

## The answer in short

Release native code is level with C and Rust and has no large lever left on
the benchmark rows. The large gains are elsewhere:

- **The interpreter**: about 2.5 to 4 times from changes that keep its
  design, the first of them a one-hour change to how the Go collector is
  set.
- **Allocation on several threads**: 5 to 8 times, from one line of the
  runtime.
- **Debug builds**: most of their distance from release is the optimization
  level clang is given.
- **The toolchain's latency**: the IR validator is about 60% of the release
  front end; the language server's outline request is quadratic; the
  repository's test suite builds about 690 programs one after another.
- **The library**: `Map`, `Vec.clone`, byte comparison and search, float
  formatting, file reading.

## How it was done

Five reviewers, one area each, read-only on the repository, each required to
read the past performance reports first so as not to propose what is done
or decided. They ran on one machine at the same time (arm64 macOS, load
average 13 to 39), so the evidence is what load does not disturb:
instructions retired and cycles from `/usr/bin/time -l` (minimum of
interleaved rounds), allocation counts, emitted IR and assembly, profile
shares, and growth curves on synthetic programs. Wall times are indicative.
`bench/harness` was not run. Prototypes were made in scratch clones, never
in the repository, and are not kept with this report; where a prototype
gave a number, the change is described closely enough to redo it.

Each number below is marked:

- **checked**: reproduced or read in the source by the author of this
  report;
- **measured**: measured by a reviewer, with a prototype or an edited IR
  where it says so;
- **estimated**: argued from a measured share or count.

Not covered: Linux and x86-64 (statements about them are from reading the
code), freestanding images under QEMU, sockets and `std/process` in the
interpreter, real editor traces.

## 1. The runtimes

### 1.1 Every allocation writes one shared flag

`rt_alloc.c:173, 225, 256, 290` each begin with
`atomic_exchange(&kern_fail_next, 0)`. Only `testing.fail_next_alloc`, in a
test-hooks build, sets that flag, but the exchange is a write to one cache
line by every allocating thread (**checked** in the source).

| program | as it is | load before exchange |
|---|---|---|
| 4 threads × 5M `Box.new` and drop | 9.3–15.2 G cycles, 0.82–1.12 s | 1.4–2.1 G cycles, 0.12–0.24 s |
| the same 20M pairs on 1 thread | 1.5 G cycles, 0.40 s | unchanged |

Two reviewers **measured** it independently with their own programs and got
the same factor, 5 to 8. `rt_thread.c:190` already uses the load-first form
for the spawn hook. No benchmark workload allocates on several threads,
which is why it was never seen; httpd's connection threads and the hashing
workers of ksum and ksearch do. Effort: under an hour.

### 1.2 The allocator always calls `aligned_alloc`

`rt_alloc.c:232` (**checked**). The realloc and zeroed paths already use
`realloc` and `calloc` for an alignment of 16 or less, and `:224`, `:231`
divide where a mask would do, the alignment having just been tested to be a
power of two.

| | instructions per `Box.new`/drop pair | cycles |
|---|---|---|
| kern | 599–607 | 78 |
| kern, `malloc` when align ≤ 16 | 479–495 | 65 |
| C `aligned_alloc(8)`/`free` | 518–537 | 70 |
| C `malloc`/`free` | 408–421 | 56 |

**Measured**: −18% instructions, −17% cycles per pair; `json.parse` −9%
instructions; with 1.1, 3 to 6% of the instructions of kzip, ksum, kray and
kcache after start-up. A thread-local or size-class front end is not worth
having after this: a pair then costs what C's does. Effort: under an hour.

### 1.3 `swap_at` is an opaque call

`calls.go:808-822` emits `kern_swap(ptr, ptr, i64 size)` though the size is
a constant at the call; `rt_base.c:107` copies through a 64-byte buffer
(**checked**). **Measured** on 50M swaps of `u64`: 123 instructions and 25
cycles a swap; with two loads and two stores in the IR, 10 and 1.4. ksort on
a million lines: 5.75 G → 3.38 G instructions (−41%), output identical to
`LC_ALL=C sort`. Heaps and sorts in ksim, ksearch, kq, kzip and
`rand.shuffle` use it. Linking the runtime with `-flto` is not a substitute:
it left ksort's five sites as calls. A large element wants a constant-size
`llvm.memcpy` through an entry-block temporary. Effort: 2 to 4 hours.

### 1.4 A freestanding release build is compiled at `-O1`

`build.go:628` (**checked**); hosted release is `-O2`. No decision is
recorded. **Measured** on hosted builds of the same IR with the freestanding
flags, `-O1` → `-O2`: vector 0.188 G → 0.079 G cycles, packet_parser
1.08 G → 0.56 G, ring_buffer −45% instructions. Not run under QEMU. Effort:
one line and the QEMU suite.

### 1.5 Float formatting

`rt_str.c:16-19` tries precisions 1 to 17, each with `snprintf` and
`strtod`: 34,100 instructions for a long value where C's `%.17g` is 3,860
(**measured**, 200K random doubles); the freestanding `f64text.c` is 79,400.

- Trying 15 digits first, then 16 and 17 or 1 to 14, gives the same text
  (for a value that is not a power of two the read-back interval is
  symmetric, so "p digits read back" is monotone in p; powers of two keep
  the linear search): 11,400 a long value, identical text on 3M values, but
  short values about 35% slower. Hours.
- A shortest-digits algorithm shared by both runtimes, the search kept for
  powers of two and as the test's oracle: **estimated** under 1,000. One to
  two days. `float-text.md` defines the output as the search's result, so
  equality must be tested.

### 1.6 Small entries that are opaque calls

`Shared.get` (`calls.go:1069`, the callee returns `s + 16`), `Mutex.held`
(`:1084`), `SystemAllocator.new` (`:1187`, the address of a static, called
before every alloc and free), `container_identity` (`:1032`, zero in
release). **Measured** with `Shared.get` as a `getelementptr`: 10 → 1.75
instructions an iteration, and the loop then vectorizes. `inline-atomics.md`
lists inline `Shared` clone and drop as open. No benchmark row moves.
Effort: 2 to 4 hours.

### 1.7 `Condvar.notify_*` always makes a wake system call

`rt_sync.c:219-220`. **Measured** with no waiter: 1,490 instructions and 420
cycles for lock, unlock and notify, of which the lock pair is 54; C with
`pthread_cond_signal` is 120 in all. A waiter count, as `Channel.gave` keeps
(`sync.kern:243`), removes the call. `perf-gaps.md` tried this on
thread_pool and measured nothing, that program notifying once per 1,024
jobs; it was not adopted and not rejected for cause. Effort: 2 hours and the
TSan fixtures.

### 1.8 Standard output takes a pthread mutex per `print_*`

`rt_base.c:23, 65-77`. **Measured** with the runtime's own parking lock in
its place, 5M lines of four prints: 4.52 G → 3.20 G instructions (−29%),
−20% cycles, same bytes. kern was already 1.7 times faster than `printf`.
Effort: 1 hour.

### 1.9 System calls in files and sockets

- `io.read_from` (`io.kern:76-86`) reads 4,096 bytes at a time into a stack
  buffer and copies. **Measured** on a cached 256 MiB file: 548 M
  instructions, 246 M cycles, 65,536 reads; sized from `metadata()` and read
  into the vector, 173 M and 110 M. A file that changes size during the read
  needs the loop to go on to end of file. 1 to 2 hours.
- `kern_net_accept` then `kern_net_ready` (`rt_net.c:166-174`, `:79-98`):
  five calls a connection on macOS, where the accepted socket already has
  `O_NONBLOCK` and `SO_NOSIGPIPE` and lacks only close-on-exec; on Linux
  `accept4` sets both flags and three `fcntl`s follow it needlessly.
- `kern_net_poller_set` (`:543-573`): two `kevent` calls a watch on macOS
  (the delete of an absent filter fails each time; one call with two changes
  and `EV_RECEIPT` does both); on Linux `EPOLL_CTL_MOD` is tried first, so a
  new descriptor costs two calls.
- `io.read_dir` (`io.kern:770-787`): an `lstat` per entry where `readdir`'s
  `d_type` has the kind.
- `kern_proc_write` on Linux (`rt_proc.c:356-377`): four signal-mask calls
  around every write.

Half a day in all; the directory change needs a new entry.

### 1.10 The freestanding runtime's internals

`memcpy`, `memset` and `memmove` are byte loops (`rt.c:162-169`): 4.0
instructions and 1.0 cycle a byte against 0.29 and 0.067 for an aligned
word copy, which stays legal under `-mstrict-align` (**measured** on the
host with the shipped flags). The heap (`rt.c:270-307`) walks an
address-sorted free list twice per free: 7,100 instructions an alloc/free
pair with 12,000 live blocks. The link has no section GC (`build.go:669`): a
hello image carries the unused allocator.

### Found good or settled

The Mutex is 54 instructions an uncontended pair and inlining it gains
nothing (`perf-gaps.md`, re-measured). `Channel`, `Once` and `WaitGroup`
wake only a counted waiter. Thread spawn costs C's plus the 8 MiB stack.
Standard output has a 64 KiB buffer. `posix_spawn` is used. An empty program
retires 15.5 M instructions against C's 12.1 M, 2.8 M of it dyld binding 47
imports. `Vec.resize` from empty uses `calloc` and skips the fill. Nothing
is zeroed twice.

## 2. The library

### 2.1 `Map` against `MapIn`

`std/map.kern:46`: `Map` keeps three vectors (`Vec[u8]`, `Vec[Option[K]]`,
`Vec[Option[V]]`), `with_bits` pushes `None` twice per slot on every
rebuild, and a probe has a bounds check on each vector and tests an
`Option`'s tag twice. **Measured**, u64 to u64, 2M inserts then 8M finds:

| | instructions | cycles | peak RSS |
|---|---|---|---|
| `Map` | 1,532 M | 946 M | 283.7 MB |
| `MapIn[u64, u64, SystemAllocator]` | 510 M | 594 M | 139.6 MB |

A find is 65 instructions against 26; a slot 33 bytes against 17. Making
`Map` a wrapper over `MapIn` (the same probe, growth and slot order, so
iteration order is kept; a trap where `MapIn` returns `Err`) adds no unsafe
block. kvstore, kcache, kvm and klisp use it; the hash_table benchmark has
its own table. Effort: half a day to a day.

### 2.2 `Vec.clone`

`std/core.kern:769-773` pushes `copy(self.get(i))` per element
(**checked**); the release loop moves one byte an iteration, reloading the
capacity and storing the size each time. **Measured**, 200 clones of 1 MiB:
1,917 M instructions; written through the raw pointer with the size set
once, 38 M (LLVM emits `memcpy`). `String.clone` is this function. Effort:
30 minutes and an unsafe-audit row.

### 2.3 The first capacity of a vector, and JSON strings

`std/core.kern:491-496, 515-521`: the first capacity is 4 elements whatever
the element's size, so a byte vector reallocates at 4, 8 and 16 inside the
allocator's smallest block. **Measured**: a 12-byte vector built by pushes,
1,610 → 700 instructions with `reserve(16)` first; `json.parse` of a 2.6 MB
document makes 350,011 allocation calls a parse, 72% of its time in the
allocator, and −24% instructions with a first capacity of 16 bytes for
one-byte elements. `json.parse_string` (`std/json.kern:121-185`) pushes each
byte; scanning to the closing quote and taking one `String.from` for a plain
run is −30% (**measured** on a copy). The two overlap; about −40% together
(**estimated**). `capacity()` values in fixture outputs would change.

### 2.4 Byte comparison and search

Copy and fill loops compile to `memcpy` and `memset` at `-O2` and `-O1`
(**measured**: 0.24 instructions a byte). Comparison and search do not: an
early-exit byte loop cannot be vectorized.

| routine (200 passes over 1 MiB) | cycles a byte | instructions a byte |
|---|---|---|
| `str.equal` (`std/str.kern:260`) | 1.02 | 6.0–7.0 |
| an 8-byte `load_le` comparison in safe kern | 0.105 | 0.87 |
| a byte scan for one value | 1.0 | 6 |
| a word-at-a-time scan in safe kern | 0.126 | 1.1 |
| `memcmp`, `memchr` | 0.08 | 0.5 |
| `str.find` (`:305`), worst case | 3.5 | 31 |

`BufReader.read_line` is 13 instructions a byte and `LineBuf.read_line`
17.6, their newline scans (`io.kern:953`, `:1158`) being that loop. Either
word-at-a-time `equal`, `starts_with`, `ends_with` and a `find_byte` in
`std/str` (2 to 4 hours), or two prelude intrinsics over `memcmp` and
`memchr`, which also mends debug builds and needs word-wise versions in the
freestanding runtime (about a day).

### 2.5 `str.hash`

FNV-1a a byte at a time (`std/str.kern:271-279`): 42 instructions and 11.8
cycles for a 16-byte key against 7.7 and 1.4 for a two-multiply 8-byte hash
in safe kern (**measured**). A string-keyed lookup is 144 instructions and
63 cycles; 30 to 40% of it would go (**estimated**). Slot order, and so the
order of `for k, v in m`, changes with the hash.

### 2.6 Operations the library lacks

- **Copy within one vector.** `kzip.kern:194-197` restores a match one byte
  at a time because a view of a vector cannot be passed to its own `extend`.
- **A checksum.** kzip's CRC-32 is one table lookup a byte and `kzip -t` on
  16 MiB is 408 M instructions against 43 M for the system's `gzip -t`;
  slicing by 8 over `load_le` would cut the restore 2.5 to 3 times
  (**estimated**).
- **Removal by a borrowed key.** `Map.remove(key: own K)` makes
  `kvstore.kern:73` build a `String` per removal.
- **A sort.** ksort, ksim, ksearch, kq and kzip each write one.
- **A deque.** `Vec.remove_prefix(1)` is used as a queue's pop in kpass.

### 2.7 Small and certain

`sync.Channel` computes its ring index with `%` (`sync.kern:274, 285, 417`):
two divisions a send/receive pair of 71 instructions. `str.utf8_valid`
(`str.kern:418`) has no ASCII fast path. `rand.Rng.fill` stores a byte at a
time. `Pool.get` in release is 12 instructions with three loads and three
branches; folding `live` into the generation's parity would drop one of
each.

### Found good or settled

`Vec.get` and `at` are 5 instructions, `push` 7 on its fast path, `extend`
is `memcpy`, growth is doubling through `realloc`. `Arena.get` is 5
instructions. `Shared` clone and drop are 33 together. `const` arrays and
structs are read-only data. `LineBuf` is not quadratic.

## 3. The language as a cost

### 3.1 No square root

kray's and ksim's `root` are Newton iterations in kern. **Measured** by
doubling the iteration in a copy of kray with identical output: `root` is
36% of kray's instructions and about 41% of its cycles. `sqrt` for `f32` and
`f64` lowered to `llvm.sqrt` is correctly rounded on every target, so
"the same bytes whatever computes it" holds, as it does for `abs`, `floor`,
`ceil`, `trunc` and `fma`; `ln`, `exp` and `sin` are not and belong in kern
code if results must be identical across targets. kray about −37% cycles
(**estimated**). Half a day; a language addition.

### 3.2 What the checks cost in release

**Measured** with `--unsafe-elide-checks` against release:

| program | instructions | cycles |
|---|---|---|
| btree | −22% | −2 to −5% |
| kzip compress, 16 MiB | −24% | −13% (one reviewer), none (another) |
| hash_table | −9% | −7% |
| packet_parser | −3.6% | noise |
| vector | −0.8% | noise |
| ksum sha256, blake2s | 0 | 0 |
| kvm | −1.2% | about 0 |

Checks are cheap in cycles on this processor, as `gap-diagnosis.md` said. A
checked `+` in a reduction is one element a step where `wrapping_add` is
eight in NEON; ksum pays nothing because its cores are written in
`wrapping_*` under a budget, and there the cost is readability.

### 3.3 Bounds checks the proof pass keeps

377 runtime-checked against 664 proven across `examples/` and `bench/` at
release. By idiom, most frequent first:

| idiom | about | weight | what removes it |
|---|---|---|---|
| a sub-slice by a pair computed elsewhere (`line[w.lo : w.hi]`) | 85 | cold | the library returning a view |
| literal offsets into a slice of conventional size (`load_le(p, 12)`) | 60 | one each | an `if len(p) < N` guard today; `view[[N]u8]` from a slice |
| an index kept in a struct field (`buf[self.fill]`, `ring[g.head]`) | 25 | hot | a field invariant or range type |
| a data-dependent table index | 45 | hot | a mask, or a range type |
| heap and tree arithmetic (`xs[child + 1]`, `n.keys[i - 1]`) | 25 | hot | a range-typed field |
| `base + i` in loops | 20 | LLVM versions the loop | — |

What the prover could learn as one constant-interval rule: `for i in 0..5`
into `[8]`, `a[i + 8]` for `i in 0..8` into `[16]`, `i * 4` under
`len(p) >= 64`, `usize(b >> 4)` into `[16]`, and the length of a
`view[]u8` string constant. LLVM already deletes these at `-O2`, so the gain
is in budgets and reports, not release speed. btree's check cost is
`n.keys[i - 1]` under `count: u32`, which only an invariant on the field can
prove; it is the one item here that touches a benchmark row.

### 3.4 Unions

Every `match` lowers to a switch whose default calls `kern_trap(12)`
(`emit.go:1793`); kvm's release IR has 316 among 1,023 trap sites, and after
`Vec.pop` is inlined LLVM keeps a dead tag test on every pop. **Measured**
with the default `unreachable`: kvm −5.4% instructions, about −1% cycles. A
tag corrupted by unsafe code would then be undefined behaviour and not a
trap.

`Option` has no niche: `Option[Box[T]]` is 16 bytes, `Option[u64]` 16,
`Option[String]` 32; a list node of a `u64` and an `Option[Box[Node]]` is 24
where a niche gives 16. A struct of `u8, u64, u8, u32, u8` is 32 bytes and
16 reordered. Both are changes of the §4.1 layout.

### 3.5 Recognised already

Rotate, bitwise not, byte swap, high multiply and saturating add compile to
single instructions in release (a rotate must use `wrapping_shl`, the
checked shift trapping on bits shifted out). `load_le`, `load_be` and
`store_*` are one wide access. The `count_*` intrinsics exist. Generics are
static dispatch throughout and cost code size, not time: kvm carries 19
copies each of `Vec.grow` and `Vec.reserve` that differ only in two
constants, about 9 KB of its 96 KB.

## 4. The code kern produces

### 4.1 Large aggregates cross calls as first-class values

`emit.go:596-614, 377-383, 1776-1792, 1251-1286`; `largeThreshold = 128`
(`:392`) governs only storage inside a function. `kzip.new_tables` returns
a 2,440-byte `Tables` as `ret %"T.Tables"`, 1,001 scalar leaves.
**Measured**: `clang -O2` on kzip's release IR retires 23.5 B instructions,
60% of it in instruction selection, where examples of that size take 2 to
6 B; with that one function returning through a pointer (`sret`), 3.0 B, and
`__kern_text` 52,668 → 31,448 bytes, output unchanged. **Checked**:
`kern build --opt=release` takes 2.2 s on kzip and 0.8 s on kcache. btree's
`Pool.alloc[T=Node]` takes its 376-byte node in seven registers and about 56
stack slots. Passing and returning aggregates above a leaf count by pointer
touches function values, closure and spawn thunks and `extern "C"`
signatures. Effort: 1 to 2 days.

### 4.2 Debug builds are compiled at `-O1`

`native/build.go:220-223`. **Measured**, the same debug IR and runtime,
relative to release:

| workload | `-O1` instructions / cycles | `-O2` |
|---|---|---|
| packet_parser | 2.31 / 3.50 | 1.08 / 1.05 |
| vector | 1.56 / 2.76 | 1.23 / 1.24 |
| ring_buffer | 1.71 / 1.30 | 1.04 / 1.00 |
| wal | 1.35 / 1.11 | 1.00 / 1.01 |
| btree, hash_table, arena_ast | 1.2–1.3 | unchanged |

clang's work on the IR rises 4 to 39% (10 to 30 ms on a 200 ms build).
`--debug` already forces `-O0`. One line; the owner's decision, since every
`kern test` pays the build.

### 4.3 The five undecided rows

| workload | what was found |
|---|---|
| vector | +7% instructions, level with C or ahead in cycles; the row's interval is noise |
| btree | a loop-invariant bounds check stays inside the `slot` loop (LLVM turns `keys[i-1]` into a first-iteration test after unswitching has run). `-O3`: 0.931 of the cycles; the `-O2` pipeline twice: 0.926; C in the same run: 0.958 |
| packet_parser | nothing at `-O2`, `-O3` or with a native CPU; a profile-guided build is 0.874 (245 M cycles against Rust's 258 M) |
| thread_pool, lock_free_queue | not measurable under load; nothing new |

`-O3` for release costs 0 to 4% compile time typically (ksort +17%) and 6 to
13% text, and moves no other workload more than 1%. A kern-level hoist of a
monotone-index check that sits in a loop condition is the alternative, in
days. btree's memory is the 8-byte handle (node 376 B against 312 B), which
ADR-014 decided.

### 4.4 Profile-guided builds work today

`clang -fprofile-generate` on the `.ll`, one run, `-fprofile-use`:
packet_parser 0.874, page_frame_allocator 0.872, btree 0.978 of the cycles;
wal regresses to 1.039; four unchanged (**measured**). An opt-in
`kern build --pgo-gen` and `--pgo-use=FILE` is about a day. It does not
change the comparison, C and Rust being built without profiles.

### 4.5 A cold path inlined into a hot function

LLVM inlines `Map.grow` into `Map.insert` (internal, one call site), which
then has a 320-byte frame and six callee-saved pairs on every call where
C's has 96 bytes and three. **Measured** with `Map.grow` marked
`noinline cold`: hash_table 0.965 of the cycles. A kern-side rule (called
once, under a condition, allocating) is days, mostly design.

### 4.6 Parameter facts not emitted

`emit.go:573-594` emits only `noalias` and `readonly`: no
`dereferenceable(N)`, `align`, `nonnull`, `noundef`, nor `writable` on
`edit` and `out`. In `Map.insert`'s probe loop `keys.size` and `keys.data`
are reloaded every iteration because LICM may not hoist a conditional load
from a pointer it cannot prove dereferenceable and aligned. **Measured**:
hash_table 0.949 of the instructions, cycles within noise; the rest within
0.3%. `align` is wrong for a packed-struct field passed on from unsafe code,
and a sequence view must stay without `nonnull`. Hours.

### 4.7 The `cold` attribute

`ir.go:509-512`, `emit.go:745`. `Vec.with_capacity` calls the cold
`Vec.reserve` unconditionally, so LLVM infers `cold` for `main` in 6 of 13
workloads and stops inlining it into the entry thunk (buffer_pool 0.982
without it). And the `heap_*` entries of `ColdLibrary` never match
(**checked**: the map holds `heap_alloc`; the functions are named
`core.heap_alloc`, and `baseName` strips only the `[` suffix), so they are
emitted without `noinline cold`. Minutes.

### 4.8 Open, not understood

Passing a `Vec`'s three words by value to the cold growth call and back
(sound, an `edit` parameter being exclusive) takes vector to 0.72 of its
instructions but ring_buffer to 1.63, its `Ring` fields no longer kept in
registers; no attribute or pipeline change recovered it. A struct holding an
indexed array beside scalar fields is one alloca that SROA does not split,
so ring_buffer's present result depends on a later pass. Emitting one alloca
per top-level field for a local never used whole would make it robust;
unmeasured.

### Found good or settled

Trap paths are `noreturn cold` and out of line. `nsw` and `nuw` are derived
by LLVM from the checks. `nounwind` on everything, `inbounds`, `llvm.assume`
for proven checks and a `target datalayout` each change nothing measurable.
The kern inliner earns its place (hash_table +22% instructions without it,
vector +23%). Closures passed to a small higher-order function are
devirtualized. Atomics are inline. `-mcpu=native` changes nothing here; on
x86-64 the default is the baseline CPU, as it is for the C and Rust
references.

## 5. The interpreter

Where it stands, in instructions against a native debug build: 150 to 700
times for most programs, 1,000 to 2,400 for float code, hashing and
container-heavy code. A call costs about 2,050 instructions and a 384-byte
allocation; the cheapest specialised operation about 87 instructions.

### 5.1 The Go collector runs at its default

Nothing sets `SetGCPercent` or a memory limit (**checked**). The live heap
is about 5 MB while the interpreter allocates up to 5.6 GB, so the collector
runs at its 4 MB floor: 1,098 collections in 4 s on klisp's demo.

| program | default | `GOGC=800` | RSS |
|---|---|---|---|
| klisp | 79.6 G | 47.8 G | 42 → 140 MB |
| ksum | 13.1 G | 8.15 G | 33 → 103 MB |
| kray | 14.5 G | 8.25 G | 27 → 70 MB |
| kvm | 5.00 G | 3.15 G | 36 → 97 MB |
| ksort | 24.6 G | 18.9 G | 29 → 78 MB |
| kdb | 32.6 G | 26.2 G | 40 → 129 MB |

(**measured**, instructions retired; **checked** on ksum: 12.99 G → 8.09 G,
0.70 → 0.35 s, 34 → 97 MB.) A flat percentage costs nine times the live
size on a program with a large heap (501 MB in one probe), so the change is
a floor: re-set the percentage after each cycle to
`max(100, 100 · 64 MB / live)`, and leave it alone when `GOGC` is set.
`kern run`, `kern test` and the fixture harness. Effort: 1 to 2 hours.

### 5.2 Destroying a `Vec` of `Copy` elements

`Vec.clear` (`std/core.kern:738-746`, **checked**) takes and drops each
element, 16 interpreted operations each; LLVM deletes the loop natively.
Its share of executed operations: 99.9% of a 4 MB `read_file` and
`write_file`, ksearch 77%, kdb 33%, kzip 32%, ksort 30%. **Measured** with
the call replaced by setting the size, outputs identical: ksearch −39%
instructions, kzip −28%, ksort −23%, wal −18.5%, kq −14%, kdb −12%. The
real change is a constant-folded core predicate (`needs_drop[T]()`) used by
`clear`, `release`, `truncate` and their `In` forms, or a lowering rule; the
fill loops of `resize` are the same case. Half a day to a day.

### 5.3 Indexed places and `len` are not compiled

`compile.go:118-123` refuses any path with an index and `ir.Len` has no
compiled case; each indexed access builds a `seqRef`, a closure and a boxed
ref (`interp.go:950-956, 976-982`). In ksum they are 16.5% of operations
and 33% of the mutator's time. **Measured** with about 100 lines (a compiled
load and store for field steps then one index, and a compiled `Len`), the
equivalence tests passing: arrloop −49% instructions and −86% allocations,
ksum −31%, wal −17%, thread_pool −10%, ksort −8%. Half a day.

**5.1, 5.2 and 5.3 together, measured:** ksum −58%, kzip −50%, ksort −49%
(wall 1.72 → 0.84 s), kray and kcache −43%, klisp, ksim, kvm and wal about
−40%, kdb −30%, the container benchmarks −21 to −28%.

### 5.4 Byte sequences move one boxed element at a time

`io_read` (`intrinsics.go:140-176`), `seqBytes` (`:1260`), `ptr_store_seq`
(`:764`, which is `Vec.extend`), `loadWide` and `storeWide`
(`:1223-1257`), and a byte-string constant rebuilt at every execution
(`interp.go:1168`). Reading and writing a 4 MB file is 40,520 operations but
8.05 G instructions and 20 M allocations: about 2,000 instructions a byte.
Taking the `[]byte` from the heap once and copying: under 0.2 G
(**estimated** from the counts). Half a day.

### 5.5 Frames on the Go heap

`interp.go:818-826`: a frame is one or two allocations, one slot per SSA
value with no reuse; klisp's `eval_at` has 659 slots, 10.5 KB a recursive
call. Frames are 43 to 70% of all bytes allocated and set the collection
rate; with 8 threads in kray the Go heap lock shows 13.4 s of contention
against 0.27 s on the interpreter's own. A per-thread chunked stack of
frames with stable addresses: −10 to −20% after 5.1 (**estimated**), and
better scaling. 1 to 2 days; an access must never outlive its frame, which
the checker already forbids.

### 5.6 Layouts recomputed on every raw access

`layout.Of` builds a type's string and does a `sync.Map` lookup on every
call (`layout.go:98`), reached from every raw read and write
(`rawmem.go:226`); `rawRef.field` adds a `Subst` per field step. klisp:
21.7% of the mutator in `layout.Of`, 15.5% in `decode`. A per-type
descriptor held by the compiled operation, as `destroyPlan` is: −10 to −20%
on container-heavy code (**estimated**). 2 to 3 days.

### 5.7 `Tag` on a raw place decodes the whole union

`interp.go` reads a tag as `r.get().(*Union).Tag`; on a raw place `get()`
decodes every payload field. It is the defect that was fixed in
`Interp.length` on 2026-11-12, in another place. klisp: 0.82% of operations
but 55% of all `rawRef.get` time and 7.6% of allocations. Read the tag's
bytes at the address and compile the operation: 5 to 8% on programs that
match on container elements. 1 to 2 hours.

### 5.8 The raw heap

`rawmem.go`: `alloc` only bumps and `free` deletes a map entry (`:63-112`);
`span` takes a global mutex and scans every chunk on each raw access
(`:159-176`); `handle` appends on every encode of a function value or
library object (`:207-212`). **Measured**: 80,000 allocations and frees of
16 KB reach 1,449 MB where the native build holds 1.9 MB; 800,000 pushes
and pops of a `Shared` reach 114 MB; the same million `Vec.get` calls cost
60% more with 250 chunks than with one. A long-running program under
`kern run` grows and slows without bound. Size-class free lists with a
quarantine (reuse must not hide a use after free), a lock-free chunk lookup,
and releasing a handle when its slot is overwritten. A day.

### 5.9 The rest

- Floats carry their type as a string and float arithmetic is uncompiled
  (`compile.go:404-407`); conversions and overflow checks carry strings.
  −15 to −25% on float code (**estimated**). A day.
- No operand fusion: in a scalar loop half the dispatches are
  `load local` and `const`. −15 to −25% on scalar loops (**estimated**).
  2 to 3 days.
- Unboxed scalars remain the largest structural lever (boxing is 10 to 27%
  of the mutator) and the most invasive: 2 to 4 weeks, a second
  representation. The recorded decision was "measured, not taken"; revisit
  after the rest.
- Standard output is unbuffered: 200,000 lines made 600,000 write calls.
  `docs/runtime.md` records "not buffered" as a property, so this is a
  decision to revisit, not a defect.
- A register bytecode VM would buy 1.0 to 1.3 times beyond fused operands
  and unboxed registers in the present engine, for a rewrite; a baseline JIT
  duplicates the LLVM backend. Neither is worth it.

After 5.1 to 5.9 without unboxing, a typical 2.5 to 4 times
(**estimated**), which leaves the interpreter 50 to 600 times slower than a
native debug build and still the readable reference.

## 6. The toolchain

### 6.1 The IR validator is quadratic in a function's size

`ir/validate.go:65`: `vstate.clone` copies a map of every local per block
visit (**checked**), and `opt.Run` validates five times in release. On kvm
the five validations are 79 of the front end's 135 ms, the last allocating
61 MB; one function of 8,000 lines takes 1.0 s a validation. **Measured**
with locals indexed densely and ownership kept in a byte slice (59 lines
added, 24 removed; the opt, lower, analysis and backend tests pass; kvm's
release IR byte-identical):

| release | front end before | after |
|---|---|---|
| kvm | 142.6 ms | 78.5 ms |
| selfhost_parser | 191 ms | 94 ms |
| ksim | 154 ms | 79 ms |

Every `kern run --opt=release`, `kern test` and release fixture build takes
this path. The `open` map is still cloned per block; validating only the
functions a pass changed is the next step. Effort: 2 hours.

### 6.2 The language server

- `ToPosition` (`lsp/text.go:99-100`) allocates a slice of the file's length
  and decodes the whole file on every call; the outline calls it four times
  a symbol. `documentSymbol` on kvm: 86 ms and 136 MB; 4.2 s and 5.1 GB at
  16,000 lines. A line-start table per document text and a binary search:
  about 17 ms on kvm (**estimated** from the profile share). 2 to 3 hours.
- Nothing is kept between requests and every open document is rechecked per
  keystroke (`server.go:508`; `check.go:134`): hover, definition, references
  and completion each load and resolve from scratch, 15 to 17 ms and 28 MB
  on kvm; six open documents cost 58 to 68 ms a keystroke where one costs
  31. Keeping the last resolved program per file set and content hash: hover
  about 1 ms (**estimated**). 1 to 2 days; the invalidation rule must be
  exact.

### 6.3 The repository's tests

`go test ./tests` is 395 s, of which `TestFixtures` is 299 s on one core:
about 690 clang builds one after another, two per executable fixture.

- Building every fixture's executables ahead on a worker pool, the checks
  and runs staying sequential: 117 builds took 26.6 s with one worker and
  5.5 s with eight (**measured**); the suite about 395 → 270 s
  (**estimated**). Half a day.
- `TestCompiledMatchesGenericFixtures` is 59.5 of `compiler/interp`'s 70 s;
  one interpreter pass over all 275 fixtures is 13.1 s, 11.4 s of it ten
  fixtures that wait on real time, and the test runs each four times in
  sequence. Running those ten concurrently: about 15 s (**estimated**).
- `TestSelfhostDiags` (25 s) starts the parser once per input.
- Each of the 690 builds re-runs the `clang -E -v` fingerprint, about 35 s in
  all; a harness could opt in to trusting a validated runtime object for the
  life of its process.

### 6.4 `kern build`

clang is about 90% of a hello build and 65 to 75% of a kvm build; each
Homebrew clang start is about 500 M instructions. Since `build-time.md` the
runtime grew from 833 to 4,054 lines: a cache miss is now 2.6 B
instructions and the per-build fingerprint 705 M (about 50 ms).

- Taking the fingerprint on a goroutine while the program compiles with
  `clang -c`, then linking: hello 183 → 146 ms, kvm release 708 → 660
  (**measured**, text identical). `kern test` gains the same again by
  building its two executables at once. 3 to 4 hours.
- The compiler's own collector is 30 to 40% of its CPU: `build --opt=release
  kvm` 3.65 B → 2.38 B instructions at `GOGC=400`, 35 → 46 MB. 30 minutes;
  matters on loaded or two-core machines.
- `kern check` checks library bodies though only user diagnostics are
  wanted (`cmd/kern/main.go:442`): kvm's check phase 4.07 → 1.27 ms with
  `CheckModules(prog, notStd)`. 30 minutes.
- Parallel code-generation units for debug builds (`llvm-split` on kvm's
  bitcode: 4.0 B instructions whole, 1.19 B for the largest of four parts):
  kvm debug about 0.57 → 0.3 s (**estimated**). 2 to 4 days; release would
  lose cross-unit inlining.
- The runtime object cache keys on the working directory
  (`build.go:355`): 419 MB in 7,915 files on this host (**checked**), 58
  distinct sources and 232 distinct objects among 2,704 keys. With
  `-ffile-compilation-dir=.` the fingerprint is identical across
  directories. This revises a key `build-time.md` records as reviewed.
- Bitcode in place of text IR is not worth a writer: parsing is 2 to 3% of
  clang.

### 6.5 Superlinear paths that show only at size

The checker's `flow.clone` (`check/state.go:38`): one function of 8,000
lines takes 396 ms and 753 MB. The prover's `invalidateEdits`
(`opt/prove.go:582`). The resolver's `findVariant`
(`resolve/exprs.go:350`), which scans every type of the module for each
bare `Some`, `None` or `Ok`. None matters for hand-written code (kvm's user
check is 1.3 ms). The lexer, parser, resolver, lowering and emitter are
linear in function count.

## 7. Defects found on the way

1. **A program the checker accepts fails in lowering.** `fnLower.symAt`
   (`compiler/lower/exprs.go:310`) finds the symbol of a `move x` or
   `drop(x)` by scanning every identifier of the program for the same
   position and name, and Go's map order picks among matches. Two files of
   one module with `move x` at the same byte offsets pass `kern check`, and
   `kern run` then fails with `internal: IR validation after lowering:
   m.pass_a: bb0.entry: move of %x which is uninitialized` (**checked**: 6
   of 6 runs). Files begun from one template meet it readily. The resolver
   already records the identifier (`info.Consumed[x]`), so the fix is one
   line, and it also removes a quadratic scan.
2. **`bench/REPORT.md` under-reports kern's text.** `bench/harness/main.go:415`
   reads only `Section __text`, and kern's code has been in
   `__TEXT,__kern_text` since the location markers (**checked**: kzip has
   12,404 bytes in `__text`, which is the C runtime, and 52,668 in
   `__kern_text`). Every kern row is affected.
3. **Frame pointers.** `kern.entry`, `@main`, drop and equality glue and
   spawn thunks are emitted without the `"frame-pointer"="all"` attribute
   (`emit.go:172-176`, `types.go:189`, `eq.go:27`, `calls.go:1269`). LLVM
   usually inlines `main` into the entry thunk, so on x86-64 the program's
   main body runs with `rbp` as a general register: a hole in what
   `x86-frame-pointers.md` set out to guarantee. Found in the emitted code;
   no wrong trap record was produced.
4. **The interpreter's raw heap and handle table** never give memory back
   (5.8).
5. **`ColdLibrary`'s `heap_*` names never match** (4.7).

## 8. An order to take them in

By gain against effort, each an hour or two unless said:

1. The lowering defect (7.1).
2. The allocation flag and `malloc` for small alignments (1.1, 1.2).
3. The interpreter's collector floor (5.1).
4. The IR validator (6.1).
5. `swap_at` inline (1.3).
6. `Vec.clone`, the first capacity, `read_file` (2.2, 2.3, 1.9).
7. Compiled indexed places and `Tag` in the interpreter (5.3, 5.7).
8. The `cold` attribute and the harness's text bytes (4.7, 7.2).
9. `kern check` without library bodies and the compiler's collector (6.4).
10. Then the half-day and day items: `Vec.clear` of `Copy` elements (5.2),
    `Map` over `MapIn` (2.1), aggregates by pointer (4.1), byte comparison
    and search (2.4), the language server's positions (6.2), the fixture
    builds in parallel (6.3).

## 9. Decisions that are the owner's

- Debug builds at `-O2` (4.2), and freestanding release at `-O2` (1.4).
- `-O3` for release, or a kern-level hoist, for btree (4.3).
- `sqrt` and the exactly rounded float operations as intrinsics (3.1).
- A `match` default that is `unreachable` (3.4).
- A niche for `Option` and field reordering (3.4): layout changes.
- Field invariants or range types, the one thing that would prove btree's
  remaining checks (3.3).
- A faster `str.hash`, which changes map iteration order (2.5).
- Buffered standard output in the interpreter, against a documented property
  (5.9).
- The runtime cache's key (6.4).
- Whether `kern test` runs the interpreter's release configuration by
  default; the interpreter runs set the suite's time.

## What was done (2026-11-12)

Implemented after the review, each measured again on the same loaded host
(instructions retired and cycles from `/usr/bin/time -l`; before is the
compiler at `c0634b6`):

| item | change | before | after |
|---|---|---|---|
| 7.1 | the moved variable through `Info.Consumed`, in the checker as well as the lowering (the checker had the same scan) | 6 of 6 runs fail | 12 of 12 pass; fixture `tests/run/move_same_offset` |
| 1.1 | the failure flag read before it is exchanged | 4 threads × 5M `Box.new`: 9.9–12.4 G cycles | 1.9–2.3 G |
| 1.2 | `malloc` for an alignment of 16 or less; masks for the divisions | 20M pairs on one thread: 12.15 G instructions, 1.58 G cycles | 9.90 G, 1.27–1.31 G |
| 5.1 | a 64 MiB heap floor for Go's collector (`compiler/heapfloor`), in every command but `kern lsp` and in the test binaries | ksum demo 13.07 G, klisp 80.8 G, kvm 4.96 G, kdb 32.7 G | 8.01 G, 47.0 G, 3.07 G, 26.1 G; RSS 34–46 MB → 85–90 MB; a program with a 350 MB heap is unchanged (351 → 353 MB, where a flat `GOGC=800` gives 708 MB) |
| 6.1 | the validator's locals in a slice, with "no entry" kept as a state so that joins are exactly what the map gave | `emit-ir --opt=release` kvm: 1.79 G (1.16 G with the floor alone) | 0.83 G; IR byte-identical |
| 1.3 | `swap_at` as three constant-size copies | 50M swaps: 6.18 G instructions, 1.27 G cycles | 0.53 G, 0.083 G |
| 2.2 | `Vec.clone` through the raw pointer | 200 clones of 1 MiB: 1,917 M | 38 M |
| 2.3 | a first capacity of 16 bytes for elements of one to three bytes | `json.parse`, 10 × 2.6 MB: 3,398 M (after 1.1 and 1.2) | 2,582 M |
| 1.9 | `io.read_from` sized from `fstat` | 256 MiB file: 545–595 M instructions | 173 M |
| 5.3, 5.7 | compiled indexed load and store, `Len`, and `Tag`; a raw union's tag read where it lies | ksum demo 8.01 G (after 5.1) | 5.59 G; klisp 47.0 → 43.6 G, kzip 5.97 → 5.52 G |
| 7.2 | the harness sums `__text` and `__kern_text` | kern rows count the C runtime only | both |

Taken together the interpreter runs the example demonstrations in 57%
(ksum), 46% (klisp), 40% (kvm), 29% (kzip) and 21% (kdb) fewer
instructions, with identical output.

The heap floor had one consequence to mend. Go's network poller is made
at first use and on macOS is a descriptor; it had existed by the time a
program started only because a collection during start-up made it, and
with no collection at start-up it appeared at the program's first socket.
`TestConnectsThatFailLeaveNoDescriptor`, which counts descriptors before
and after, then failed every time (and had failed now and then before,
for the same reason). The interpreter now makes the poller before a
program runs.

Left as they were, deliberately:

- **The `cold` attribute (4.7).** Making the `heap_*` names match would
  mark every allocation helper `noinline cold` for the first time, and
  dropping `cold` from `Vec.reserve` changes what LLVM infers for `main`
  in six workloads: both alter code generation on every benchmark row and
  want a quiet host to judge.
- **`kern check` without library bodies (6.4).** It saves about 3 ms of a
  check and would stop `kern check` reporting a checker diagnostic in a
  `std` file, which is how an edit to the library is first seen to be
  wrong.

`kern_swap` left both runtimes with its last caller: `TestRuntimeABI`
refuses an entry no program declares.
