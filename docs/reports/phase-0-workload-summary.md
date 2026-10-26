# Phase 0 — §8 workload classification summary

**Inputs:** `docs/workloads/*.md` (one note per §8 workload), spec v0.2 §5.6, §5.8, §8, §11.
**Rule applied:** a workload is "expressible in the core" iff its own source
(`bench/kern/<name>/`) contains no `unsafe` block and no `view struct`; it
"requires audited unsafe internals behind a safe API" iff its own source must
contain an enumerated unsafe operation (§6) behind a contract-bearing safe
function; it "requires the §5.6 reserved extension" iff a correct or
10%-competitive implementation needs a reference-holding aggregate. Standard-
library containers (`Vec`, `Box`, `Arena`, `Pool`, `Mutex`, `Shared`, `Atomic`)
carry their own audited unsafe internals and are row (b) of §2.3; using them
does not count against a workload.

| # | Workload | Classification | Unsafe in workload source | Cursor rewrite needed |
|---|---|---|---|---|
| 1 | vector | expressible in the core | none | no (`items()` projection) |
| 2 | hash_table | expressible in the core | none | yes: index cursor `first`/`next` |
| 3 | btree | expressible in the core | none (`Pool.get2` in std) | yes: `Cursor { node, slot }` for range scan |
| 4 | arena_ast | expressible in the core | none | no (handles) |
| 5 | wal | expressible in the core | none (assumes `std/fs`) | yes: byte-offset replay cursor |
| 6 | buffer_pool | requires audited unsafe internals behind a safe API | `pin` (runtime-exclusive escalation, §5.3), eviction interior write | no |
| 7 | packet_parser | expressible in the core | none | yes: `Layout` offsets instead of `Parser { input: view }` |
| 8 | ring_buffer | expressible in the core | none | no |
| 9 | page_frame_allocator | expressible in the core | none (`Allocator` adapter outside the workload) | no |
| 10 | intrusive_run_queue | requires audited unsafe internals behind a safe API | `RawPtr` links and `deref` (§6) | no (handle API) |
| 11 | thread_pool | expressible in the core | none (`Mutex.lock` in std) | no |
| 12 | lock_protected_map | expressible in the core | none | no |
| 13 | lock_free_queue | requires audited unsafe internals behind a safe API | interior writes through `view` under `unsafe assert Interior` (§5.5, §6) | no |

## Counts
- Expressible in the core: **10**.
- Requires audited unsafe internals behind a safe API: **3** (buffer_pool,
  intrusive_run_queue, lock_free_queue), each the case §5.3, §5.8, or §5.5
  itself designates for unsafe internals.
- **Requires the §5.6 reserved extension (view-kinded aggregates): 0.**

Per §5.6 and §11, the extension is scheduled for Phase 1 iff the count is at
least one; with a count of 0 it is **not scheduled**, and PLAN.md 1.5 is
skipped with this report as the recorded reason. Phase 0's "at most two"
exit criterion is met.

## Where the extension was closest to being needed
Five workloads use the cursor-handle rewrite instead of a borrowing iterator or
reference-holding parser (hash_table, btree, wal, packet_parser, and the
`peek` form of ring_buffer). In every case the rewrite is a `Copy` index or
offset re-derived through the single-source elision rule (§5.3), costing one
bounds or generation check per step and no allocation or copy. None needs the
extension for correctness; whether any needs it for the 10% target is a Phase 2
measurement (`show-bounds-checks` provenance on the re-slice and `get` calls),
and packet_parser is the one to watch.

## Caveats that could change a row
- wal: classified assuming `std/fs` provides `Storage`; otherwise the workload
  carries about ten `unsafe` FFI lines and moves to the unsafe-internals row.
- page_frame_allocator: the same if the harness requires `impl Allocator` in
  the workload itself (one unsafe cast).
- buffer_pool: depends on the release-function signature receiving a per-frame
  receiver (`Frame.pin`), to be fixed in the Phase 1 library reference.
- intrusive_run_queue: depends on `RawPtr[T].deref() -> edit[T]` being given a
  fresh root inside `unsafe` (PLAN 2.3 design item).
None of these caveats involves the §5.6 extension; the extension count is 0
under every resolution.


## Review notes (prism, 2026-10-26)

The sketches in `docs/workloads/*.md` are classification evidence, not implementations. The prism review of Phase 0 flagged the following design points in them; each is carried as an open risk for the phase that implements the workload (2 for the containers, 3 for the concurrent ones), and none changes a classification.

- **buffer_pool:** `fetch` returning an unreserved frame lets another thread evict it before `pin`; reservation must be taken under the page-table lock and `pin` must verify page identity. Dirty marking belongs inside the write-pin's release, not after `unpin`.
- **intrusive_run_queue:** the safe wrapper must enforce that the task table belongs to the queue and that a task is unlinked before enqueueing, or those operations become `unsafe`; freeing a linked task through the table API must be prevented.
- **lock_free_queue:** capacity must be a power of two greater than one; sequence numbers need wrapping arithmetic and a signed modular comparison.
- **page_frame_allocator:** `free_frame` must check against the configured frame count, not only the bitmap capacity.
- **thread_pool:** `Stop` markers must not overtake pending work (use a FIFO or a shutdown flag); `shutdown` must handle allocation failure when enqueueing markers; a failed later `spawn` must stop already-started workers.
- **hash_table:** insertion must reuse tombstones and must not increment `count` when replacing an existing key.
- **btree:** range-scan cursors need an ancestor stack or parent links to find successors across node boundaries; the one-`get`-per-step claim is withdrawn until measured.
- **wal:** encoding must reserve or roll back on partial-record failures, and a sync failure must not re-append the staged bytes.
- **lock_protected_map:** `parallel_count` must count the actual input keys, validate `threads > 0`, and cover the remainder of the input.
- **packet_parser:** the UDP checksum must be validated over the pseudo-header when nonzero.
- **show-bounds-checks example:** the three-entry output claim omits arithmetic overflow checks; the machine-API example is to be re-derived from the IR provenance in Phase 1.
