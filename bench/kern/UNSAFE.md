# Unsafe blocks in bench/kern (PLAN.md 3.4 audit)

Every `unsafe` block and `unsafe assert` in `bench/kern`, with the function
it sits in, the contract block on that function (Rule 6.4), what the block
does, and the fixture or benchmark run that exercises its failure path.

No other benchmark program contains `unsafe`: vector, hash_table, btree,
arena_ast, ring_buffer, wal, packet_parser, packet_parser_simd, buffer_pool
(and its `_fail` twin), page_frame_allocator (and its `_fail` twin),
thread_pool and lock_protected_map are safe code. Their only runtime
escalations are `Mutex.lock` (inside `std`, Rule 5.3.6) and the buffer
pool's `pin(edit self, f) -> edit[Frame] release unpin_last`, which is safe:
it returns a `Vec.at` projection of its `edit self` receiver and the
release function unpins when the caller's block ends (every exit of the
block, including the early `return 1i32` on a page-number mismatch).

## intrusive_run_queue/intrusive_run_queue.kern

The tasks are a raw array of 1024 `Task` values (32 bytes each, §4.1
layout: `next` at 0, `prev` at 8, `prio` at 16, `id` at 24) allocated from
`SystemAllocator`; the run queue stores raw addresses into it. Every block
is covered by the C comparison: the per-op `id XOR prio` sum and the final
walk (`cs += (k + 1) * (id + 1)`, `k` must reach 1024) print the same
checksum as `bench/c/intrusive_run_queue` (`51548582311292`) in release and
debug builds, so a wrong offset, a lost link or a double link cannot go
unnoticed.

| Function | Contract | What the block does | Failure path exercised by |
|---|---|---|---|
| `next_of(p)` | requires "p addresses a live Task in the task array" | casts `p` to the address of its `next` field (offset 0) | none at runtime: address arithmetic only; a wrong offset is a checksum mismatch against bench/c |
| `prev_of(p)` | same | byte offset 8, cast to `RawPtr[RawPtr[Task]]` | same |
| `prio_of(p)` | same | byte offset 16, cast to `RawPtr[u64]` | same |
| `id_of(p)` | same | byte offset 24, cast to `RawPtr[u32]` | same |
| `task_at(base, i)` | requires "base addresses an array of at least i + 1 Tasks" | `ptr_offset(base, i)` | the only caller is the setup loop `for i in 0..1024`; the allocation is 1024 tasks |
| `init_task(t, prio, id)` | requires "t addresses writable storage for one Task that is not linked" | `ptr_store` of a whole `Task` with null links | setup only; a linked task is never re-initialized (`insert_sorted` follows immediately) |
| `task_id(t)` | requires "t addresses a live Task" | `ptr_load` of `id` | checksum |
| `bump_prio(t, by)` | requires "t addresses a live Task"; ensures "returns the new priority" | loads `prio`, adds (trapping `+`, Rule 4.1.2), stores | overflow would trap `overflow`; 10M ops of at most 1087 cannot reach it |
| `insert_sorted(q, t)` | invariant "every Task reachable from q.head is in the task array, linked exactly once, prev/next mutually consistent, priorities non-decreasing from head to tail"; requires "t addresses a live Task that is not linked"; ensures "t is linked after the last task whose priority is not greater than t's; q.len grew by one" | walks `prev` from the tail, writes `t.prev`, `t.next`, the predecessor's `next` (or `q.head`) and the successor's `prev` (or `q.tail`) | a violated invariant shows as `q.len != 1024` or a walk with `k != 1024` after the loop (exit 1, no line), or as a checksum mismatch against bench/c |
| `pop_head(q)` | invariant "as insert_sorted"; ensures "the former head is unlinked and returned (null when the queue was empty); q.len shrank by one" | reads `head`, relinks the new head (or clears `tail`), nulls the popped links | an empty queue returns null; the benchmark keeps `len == 1024` (checked after the loop), so the null is never dereferenced |
| `walk(q, cs)` | invariant "as insert_sorted"; ensures "w.k is the number of linked tasks" | follows `next` from `head`, reading `id` | `k != 1024` exits 1 |
| `main` (allocation) | requires "the allocator obligations of §6 hold; the task array outlives every link into it" | `SystemAllocator.alloc(32768, 8)`, cast to `RawPtr[Task]`; builds the empty queue with `ptr_null` | `Err(OutOfMemory)` returns exit code 2 before any link exists |
| `main` (release) | same | `SystemAllocator.free` of the array after the final walk | none: the queue is not used afterwards |

## lock_free_queue/lock_free_queue.kern and lock_free_queue_fail.kern

The two files are the same program with `FAIL_MODE` false/true. `Spsc` is
the ring: `slots: [4096]u64`, `head`, `tail` and `go` as `Atomic[u64]`
padded 128 bytes apart (inline cells: every atomic operation is an LLVM
instruction on the field). Each side keeps its own index in a private
`Cursor` and publishes it every 64 elements and before a wait, as
bench/CHECKSUMS.md specifies. Threads only ever hold a `view[Spsc]`; the
slots are written and read through that view inside `unsafe`, which Rule
6.4 permits in the declaring module under `unsafe assert Interior`.

| Declaration / function | Contract or obligation | What it does | Failure path exercised by |
|---|---|---|---|
| `unsafe assert Interior for Spsc` | obligation (Rule 5.4.6): every write through a `view` of `Spsc` is made data-race-free by the type's own synchronization, here the push/pop index protocol below | lets `push`/`pop` write and read `slots` through a `view` | the consumer's chained checksum (`9933137394116826624`, matching bench/c) in release and debug builds, repeated runs; a torn or stale slot read would change it |
| `unsafe assert Share for Spsc` | obligation (Rule 5.5.1): a `view` of `Spsc` may be read from several threads; the only interior state is the atomics and the slots covered by the assertion above | lets the scoped consumer receive `view[Spsc]` while the main thread keeps using it | same |
| `push(q, c, v)` | invariant "slot (pos & 4095) is written only by the single producer and only while pos - head < 4096, head being the consumer's release-stored value read here with acquire, so the consumer's last read of that slot happens-before the write; the release store of tail (every 64 elements, and publish_tail before a wait) publishes the slots written so far"; ensures "true: v is the newest element of the ring; false: the ring was full and v was not stored" | `q.slots[pos & 4095] = v` between the fullness check and the batched release store of `tail` | push on a full ring returns `false` before the block runs: `lock_free_queue_fail.kern` (the consumer is gated while 5120 pushes are attempted; exactly 1024 fail and the line ends with ` failures=1024`, checksum `16524715534916428288` as bench/c with `KERN_BENCH_FAIL=1`) |
| `pop(q, c)` | invariant "slot (pos & 4095) is read only by the single consumer and only while pos < tail, tail being the producer's release-stored value read here with acquire, so the producer's write of that slot happens-before the read; the release store of head (every 64 elements, and publish_head before a wait) returns the slots read so far to the producer"; ensures "Some(v): v was the oldest element and head advanced; None: the ring was empty" | `v = q.slots[pos & 4095]` between the emptiness check and the batched release store of `head` | pop on an empty ring returns `None` before the block runs: every spin of the consumer in both runs (the sampled pop includes those spins), and the gated start of the fail twin |

The acquire/release pairing itself is the subject of the litmus fixtures
`tests/concurrency/litmus/message_passing.kern` (release store observed by
an acquire load carries the data) and `store_buffering.kern`;
`tests/concurrency/race_raw_ptr.kern` is the deliberate-race twin that a
TSan run must catch.
