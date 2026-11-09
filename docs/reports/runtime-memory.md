# Runtime, phase 6: memory

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area F (RF1 to
RF3). **Plan:** `specs/runtime/PLAN.md`, phase 6. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

## What changed

- **A debug build checks `free` and `realloc` (RF1).** The hosted runtime
  keeps a table of the live blocks of the system allocator: the address,
  the size and alignment asked for, and the location of the call. A `free`
  or `realloc` whose pointer is not a live block, or whose size or
  alignment is not the block's, traps `explicit` at the call. A double
  free, a free of an address inside a block and a wrong size are found
  where they happen.
  - The table is 64 open-addressed tables, each under its own parking
    lock, chosen by the address's hash; a block is removed by closing the
    gap, so there are no tombstones.
  - `realloc` takes the block out of the table before the allocator may
    free it and records it again afterwards, so another thread that is
    given the same address in between finds its slot free.
  - The interpreter makes the same checks; it kept the size of each block
    and now keeps the alignment too.
  - A release build has no table: the code of its allocator is what it
    was.
  - The cost in a debug build: five million `alloc` and `free` pairs and
    five million `Vec.push` calls take 0.12 s where they took 0.10 s.
- **An allocation report (RF2).** A hosted debug build run with
  `KERN_ALLOC_REPORT=1` writes one line to standard error when the program
  returns from `main` or calls `exit`:
  `{"alloc":{"total":9,"live":4,"live_bytes":4480,"peak_bytes":4480,"sites":[{"file":"t.kern","line":21,"count":1,"bytes":4096},{"file":"t.kern","line":30,"count":3,"bytes":384}]}}`.
  - With the variable set the runtime's own objects are recorded in the
    table too, each at the call that made it, and four counters are kept.
    Without it nothing is counted.
  - A site is a file and a line. A block the library allocates carries
    the user's call, as a trap inside the library does.
  - The three `try_new` entries gained the call's location, which the
    other allocating entries already had.
  - A thread is one allocation now (its record and its block of arguments
    and result were two), so every runtime object is one block.
- **`std/mem` (RF3).** `page_size()`, and `PageAllocator`, an `Allocator`
  that maps each allocation from the operating system as whole zeroed
  pages and unmaps it on `free`. It is kern code over two unsafe
  primitives, `mem_map` and `mem_unmap`. An alignment above the page size
  is `Err(OutOfMemory)`; a request that breaks an allocator's obligations
  traps `explicit`, as the system allocator's does.

## Requirement to test

| Clause | Test |
|---|---|
| RF1: `free` with a wrong size, a wrong alignment, twice, of an address no `alloc` returned | `tests/trap/sysalloc_free_wrong_size.kern`, `sysalloc_free_wrong_align.kern`, `sysalloc_free_twice.kern`, `sysalloc_free_unallocated.kern` |
| RF1: `realloc` with a wrong old size | `tests/trap/sysalloc_realloc_wrong_size.kern` |
| RF1: the same calls with the block's own values pass (growing and shrinking `realloc`, `alloc_zeroed`, an empty block) | `tests/trap/sysalloc_checked_ok.kern`, the twin of the five |
| RF1: at the call; the interpreter traps on the same conditions | the fixtures run in the interpreter and as a debug build, and the harness compares the two records |
| RF1: a release build does not check | the fixtures are `config: opt=debug`; `TestRuntimeABI` compiles the release runtime, whose `free` is the allocator's alone |
| RF1: the library containers pass the checks | the whole corpus (`TestFixtures`: every runnable fixture as a debug build), `TestSelfhostParser`, `TestFixturesSanitized` |
| RF2: off unless `KERN_ALLOC_REPORT=1` | `TestAllocReport/off_unless_asked_for` |
| RF2: the line's shape; `total`, `live`, `live_bytes`, `peak_bytes`; runtime objects counted | `TestAllocReport/the_counters_and_the_sites` (a `Shared`, a `Mutex`, a vector and a thread made and destroyed, then blocks kept and freed) |
| RF2: sites: file, line, count, bytes; a block the library allocates and a runtime object belong to the user's call | same; `TestAllocReportAtExit` |
| RF2: a program ended by `exit` is reported | `TestAllocReportAtExit` (status 3, two live blocks) |
| RF2: a reallocation stays with the allocating call and counts in `total` only when it moved | `TestAllocReportRealloc` |
| RF2: at most 16 sites, by bytes, ties by file then line | `TestAllocReportSiteOrder` (20 sites, every size twice) |
| RF2: counters under several threads | `TestAllocReportThreads` (four threads, 1,000 blocks freed and 10 kept each) |
| RF2: a release build writes nothing; a trap is not followed by a report | `TestAllocReport/a_release_build_writes_nothing`, `TestAllocReportNotAfterTrap` |
| RF3: the page size; zeroed whole pages at a page-aligned address; `free`; an empty request; an alignment above the page size | `tests/run/mem_pages.kern` (`size`, `raw`), both engines, debug and release |
| RF3: the allocator of `VecIn`, `BoxIn`, `ArenaIn`, `PoolIn`, `MapIn` | same (`containers`) |
| RF3: `mem_map` and `mem_unmap` are unsafe; hosted only | `tests/negative/mem_map_unsafe.kern` and twin; `TestHostedOnlyInventory` |

## Deviations and open points

- **When the table itself cannot grow.** A new block that cannot be
  recorded is given back and the call is `Err(OutOfMemory)`. A block that
  `realloc` has already moved cannot be given back; the runtime then stops
  treating an unknown address as an error (a block that is found is still
  checked). No trap is added, so the `try_` forms keep their promise.
- **A failed call changes nothing** holds for the counters; a `realloc`
  that fails puts the block back in the table.
- **The report reads the table while other threads may run** (a detached
  thread at `exit`): the line is then a snapshot, each stripe consistent.
- **`peak_bytes` of the spec's sample program** is checked where the
  program's large blocks dominate, since the sizes of runtime objects are
  not part of the interface.
- **`PageAllocator.alloc` traps `explicit`** for an alignment that is not
  a power of two or a size that is not a multiple of it; the spec names
  only the alignment above the page size.
- **A scope's table of joins** is the runtime's own memory and is not
  counted.

## Verification

`go test ./...` passes on macOS; the phase's fixtures and tests, the
area-A checks, the concurrency fixtures (the table under the thread
sanitizer) and the sanitized corpus pass in the Linux container;
`golangci-lint run ./...` reports no issue.
