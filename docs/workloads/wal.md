# Workload: wal

## Summary
A database write-ahead log: append variable-length records with a length and
CRC32 header into an owned byte buffer, flush and sync through a storage
interface, replay from a byte offset, and truncate a torn tail. The I/O is a
`Storage` interface whose hosted implementation (`std/fs`) wraps FFI; the
WAL's own logic is pure core code.

## Classification
expressible in the core

## Data structures and ownership graph
```text
interface Storage { func append(edit self, data: view[]u8) -> Result[(), IoErr]; func sync(edit self) -> Result[(), IoErr]; func read_at(view self, off: u64, dst: edit[]u8) -> Result[usize, IoErr] }
struct Wal[S: Storage] { store: S; buf: Vec[u8]; next_lsn: u64; durable_lsn: u64 }
struct Record { lsn: u64; len: u32; crc: u32 }       // Copy header, 16 bytes per show-layout
```
- `Wal` owns the storage object and the staging buffer. Records are bytes in
  `buf` until `flush`; nothing holds an access into `buf` across an append.
- Replay produces owned `Record` headers plus a byte offset; a payload is
  handed to the consumer as `view[]u8` for one call (elision over the single
  buffer source).
- A `Vec[u8]` grows under the single `edit`; the interior-pointer case cannot
  arise.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `append(edit self, payload: view[]u8) -> Result[u64, IoErr]` | `edit` + `view` | two different roots: `self` and the caller's payload; never conflict |
| `flush(edit self) -> Result[(), IoErr]` | `edit` | `self.store.append(self.buf.items())`: `self.store` (edit) and `self.buf` (view) are disjoint paths |
| `replay(view self, from: u64, sink: func(Record, view[]u8) -> bool) -> Result[u64, WalErr]` | `view` | cursor is a byte offset |
| `payload(buf: view[]u8, off: usize, len: usize) -> view[]u8` | elision | sub-slice projection |
| `truncate_torn(edit self, at: u64)` | `edit` | shrinks the buffer, bumps nothing else |
No `get2`; no release accessor. The replay callback is a plain function pointer
with the record passed by value and the payload as a non-escaping `view`
(§4: function values do not capture).

## Why this classification
Appending is `Vec[u8].push`/`extend` under one `edit`; parsing on replay is
bounds-checked slicing of a `view[]u8`, which §5.2 defines with `len`, `v[i]`
and `v[i:j]`. The single-source elision rule (§5.3) returns the payload
projection from the one `view` parameter, so no aggregate ever stores a view.
The only external effect is the `Storage` call, which is an `edit self` method
on an owned value. Nothing in §5 forces `unsafe` into `bench/kern/wal/`. The
hosted `std/fs.File` implementation of `Storage` calls `extern "C"` `write`,
`fsync`, `pread` inside `unsafe` with a contract block (§6, §7); that is
library code.

## Sketch
```text
module wal_bench

impl[S: Storage] Wal[S] {
    func append(edit self, payload: view[]u8) -> Result[u64, IoErr] {
        lsn := self.next_lsn
        hdr := Record{ lsn: lsn, len: u32(len(payload)), crc: crc32(payload) }
        put_u64(self.buf, hdr.lsn)?                      // self.buf: edit, hdr: Copy
        put_u32(self.buf, hdr.len)?
        put_u32(self.buf, hdr.crc)?
        self.buf.extend(payload)?                        // roots self and payload differ
        self.next_lsn = lsn + 1u64
        if self.buf.len() >= 1048576 { self.flush()? }
        return Result.Ok(lsn)
    }

    func flush(edit self) -> Result[(), IoErr] {
        self.store.append(self.buf.items())?             // self.store edit, self.buf view: disjoint
        self.store.sync()?
        self.buf.clear()
        self.durable_lsn = self.next_lsn
        return Result.Ok(())
    }
}

func replay(data: view[]u8, sink: func(Record, view[]u8) -> bool) -> Result[usize, WalErr] {
    off := 0
    while off + 16 <= len(data) {
        hdr := read_header(data[off : off + 16])         // sub-slice projection, consumed here
        end := off + 16 + usize(hdr.len)
        if end > len(data) { return Result.Ok(off) }      // torn tail: stop at last good offset
        if crc32(data[off + 16 : end]) != hdr.crc { return Result.Err(WalErr.BadCrc(hdr.lsn)) }
        if !sink(hdr, data[off + 16 : end]) { break }   // view passed, not stored
        off = end
    }
    return Result.Ok(off)
}
```

## Benchmark notes
- C: the same record format over `write(2)`/`fsync(2)` with a 1 MiB staging
  buffer; replay with `pread` into a buffer and the same CRC32 (table-driven,
  no hardware CRC in any version).
- Rust: `std::fs::File` with `BufWriter` disabled (explicit `Vec<u8>` staging)
  and `sync_all`.
- Measure: append throughput for 100-byte and 4 KiB records with and without
  `sync` per record (the sync-bound case measures nothing about the language and
  is reported separately), replay throughput in MiB/s, p99 append (flush
  spikes), peak RSS, allocation count (staging buffer growth only), copies
  (one memcpy per append into the staging buffer, same as C), unsafe lines
  (zero outside `std/fs`), text bytes, compile time.
- Failure paths: `IoErr` from `sync` leaves `durable_lsn` unchanged; a CRC
  mismatch mid-log returns the offset of the last good record; both fixtures.

## Open risks
- `std/fs` is a Phase 2 deliverable not named in PLAN.md 2.3; if it is absent
  the benchmark must carry its own `extern "C"` wrapper, which adds a contract
  block and about ten `unsafe` lines to the workload and would reclassify it
  as "requires audited unsafe internals behind a safe API". The classification
  above assumes `std/fs` exists.
- Zero-copy replay hands `view[]u8` to the sink; a sink that wants to keep the
  payload must `copy` it, which the C version avoids by keeping the pointer.
  That copy is a semantic difference the report must state.
