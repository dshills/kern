# Today's native changes, one at a time

**Date:** 2026-10-31. **Changes measured:**
- the function location markers and kern text section (259a89e,
  `specs/native-trap-loc`);
- inlining library bodies in kern's IR (67fe69d, `specs/inline-library`);
- inlining functions that return an access (f2e6b7c,
  `specs/inline-access`).

## Method

The host's load was between 6 and 35 all day, so wall times are not
comparable. Instructions retired (`/usr/bin/time -l`) do not depend on the
load: they count what the program does, not how long it waits. Two
caveats:
- the threaded workloads (`thread_pool`, `lock_*`) spin and park, so
  their counts move by up to 2% from run to run;
- a change of layout or cache behaviour can change speed without
  changing the count, which only a quiet-host wall-clock run (the
  harness re-run, still owed) shows.

Four compilers were built from HEAD (ac0e893): everything on, and each
change switched off alone. Every benchmark's main program was compiled
in release by each compiler, and the four binaries were run alternately
five times. The table gives the median instructions retired with the
change off, divided by the median with everything on: above 1, the
change saves instructions; below 1, it costs them. Text sizes are
`__text` plus `__kern_text`. Every binary printed its workload's
checksum.

The last column is the same comparison for library inlining after the
fix below.

| workload | markers off | library inlining off | access inlining off | library inlining off, after the fix |
|---|---|---|---|---|
| arena_ast | 1.0002 (+128 B) | 1.0081 | 1.0008 | 0.9996 |
| btree | 0.9991 (+128 B) | 1.0029 | 1.0000 | 1.0045 |
| buffer_pool | 1.0000 (+144 B) | 1.0918 | 1.0927 | 1.0924 |
| hash_table | 0.9990 (+144 B) | 1.0000 | 1.0001 | 0.9995 |
| intrusive_run_queue | 1.0000 (+68 B) | 0.9999 | 0.9997 | 1.0001 |
| lock_free_queue | 0.9967 (+80 B) | 0.9962 | 0.9939 | 0.9998 |
| lock_protected_map | 1.0168 (+144 B) | 1.0048 | 0.9818 | 1.0167 |
| packet_parser | 1.0001 (+96 B) | 1.0000 | 1.0000 | 0.9999 |
| page_frame_allocator | 1.0002 (+80 B) | 1.0002 | 1.0002 | 1.0000 |
| ring_buffer | 1.0000 (+64 B) | 1.0000 | 1.0000 | 1.0000 |
| thread_pool | 0.9828 (+96 B) | 0.9877 | 1.0028 | 0.9835 |
| vector | 1.0000 (+80 B) | 0.7805 | 0.9999 | 1.0001 |
| wal | 0.9999 (+80 B) | 1.0036 | 0.9999 | 1.0038 |

## Findings

- **Markers.** They cost no instructions: every ratio is within the
  threaded workloads' noise. Each binary's code grows by 64 to 144 bytes
  (16 bytes per kern function).
- **Library inlining had one regression.** `vector` executed 28% more
  instructions with it (455 million against 355 million), and its push
  loop's timed region took 20.1 ms against 15.8 ms in the same rounds.
  - Excluding one library function at a time found the cause:
    `Vec.push` alone.
  - Left a call, `Vec.push` takes its receiver as a `noalias` parameter.
    LLVM inlines it and keeps that fact, so the element store cannot
    clobber the vector's size, capacity and data pointer, which stay in
    registers. Copied in kern's IR, the fact is gone, and every store
    reloads the header.
- **Library inlining's gain.** `buffer_pool` saves 9%, and the same
  exclusions showed all of it comes from copying `Vec.get`, which only
  reads through a raw pointer.
- **Access inlining.** It saves 9% on `buffer_pool` (the same
  `Vec.get`); elsewhere it is neutral.

## The fix

The library inliner no longer copies a function that writes through a
raw pointer (`ptr_store`, `ptr_take`, `ptr_copy`, `ptr_store_seq`,
`ptr_store_volatile`, `ptr_init`, `ptr_init_ctx`; `writesRaw` in
`compiler/opt/inline_library.go`). Such a function stays a call, which
LLVM inlines with its receiver still `noalias`. With the fix:
- `vector` is back to 355.6 million instructions, and `buffer_pool`
  keeps its 9%.
- No workload retires more instructions with library inlining than
  without it, beyond the threaded workloads' noise (last column).
- **Cost.** `arena_ast` retires 1.2% more than with every library body
  copied, since `Arena.alloc` writes through a raw pointer and is no
  longer copied. Its gap to Rust is in instructions per cycle (it
  already retires fewer instructions than Rust), so that is item 3's
  concern.
- **Same rule in the interpreter.** It applies in both engines, because
  `kern test` lowers one module for the interpreter and native code, so
  the interpreter no longer copies `Vec.push` either.

## Verification

- **`TestInlineLibrary`** now requires `Vec.push` to stay a call and
  `Vec.get` to be copied. Each copied trap must be at the call it was
  copied for: `expect`'s, and `Vec.get`'s bounds trap.
- **Gates.** `go test ./...` passes; `golangci-lint run ./...` reports 0
  issues.

## Still owed

A wall-clock comparison on a quiet host: the harness re-run (deferred
today) covers it.
