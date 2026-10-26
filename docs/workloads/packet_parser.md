# Workload: packet_parser

## Summary
A zero-copy network packet parser: given a received frame as `view[]u8`, parse
an Ethernet, IPv4, and UDP header chain, validate lengths and checksums, and
deliver the payload as a sub-slice without copying. Parsers are the classic
reference-holding aggregate (`Parser<'a> { input: &'a [u8] }`) that the core
deliberately cannot express; this note shows the rewrite.

## Classification
expressible in the core

## Data structures and ownership graph
```text
struct EthHdr { dst: [6]u8; src: [6]u8; ethertype: u16 }                  // Copy
struct Ipv4Hdr { ihl: u8; total_len: u16; proto: u8; src: u32; dst: u32 }   // Copy
struct UdpHdr { src_port: u16; dst_port: u16; len: u16; csum: u16 }          // Copy
struct Layout { l3: usize; l4: usize; payload: usize; end: usize }          // Copy offsets
```
- The packet bytes are owned by the receive ring (the ring_buffer workload or
  a `Vec[u8]`); the parser owns nothing. Every parsed header is a `Copy`
  struct built by value from bounds-checked byte reads.
- `Layout` holds offsets, not views: it is the cursor-handle rewrite of the
  reference-holding parser. The payload is re-derived as `pkt[l.payload : l.end]`
  by whoever holds the packet, in the statement that consumes it.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `parse(pkt: view[]u8) -> Result[Layout, ParseErr]` | `view` | returns `Copy`; no access escapes |
| `payload(pkt: view[]u8, l: Layout) -> view[]u8` | elision, single `view` source | sub-slice projection, consumed in one statement |
| `eth(pkt: view[]u8) -> Result[EthHdr, ParseErr]` | `view` | reads `pkt[0:14]` |
| `deliver(port: u16, body: view[]u8)` | `view` | non-escaping |
| checksum `csum(data: view[]u8) -> u16` | `view` | `for b in data` copies bytes |
No `edit` is needed on the receive path; no `get2`; no release accessor. A
parser that mutates the packet in place (NAT rewrite) takes `edit[]u8` and is
the same shape.

## Why this classification
The projection rule (§5.3) lets `payload` return a sub-slice of its single
`view` parameter, and the consumer binds it with `with body := payload(pkt, l)`
for exactly one block. The struct that holds a view (`Parser { input: view[]u8; pos: usize }`)
is `ACC_ESCAPE_STORE` (§5.2); the rewrite is `Layout` with offsets, and every
method takes `pkt: view[]u8` again. This costs nothing at runtime (a view is
already a pointer and a length; an offset is one integer) and only one extra
bounds check per re-slice, which `dominated_by_check` can remove when
`parse` already validated `l.end <= len(pkt)`. This is precisely evaluation §3
item 1, and it is the workload most likely to argue for the §5.6 extension;
the rewrite is acceptable here because the parser has no state beyond offsets.

## Sketch
```text
module packet_bench

func parse(pkt: view[]u8) -> Result[Layout, ParseErr] {
    if len(pkt) < 14 { return Result.Err(ParseErr.Short) }
    e := eth(pkt[0 : 14])?                                   // Copy header out of a sub-slice
    if e.ethertype != 2048u16 { return Result.Err(ParseErr.NotIpv4) }
    l3 := 14
    if len(pkt) < l3 + 20 { return Result.Err(ParseErr.Short) }
    ip := ipv4(pkt[l3 : l3 + 20])?
    l4 := l3 + usize(ip.ihl) * 4
    end := l3 + usize(ip.total_len)
    if end > len(pkt) || l4 + 8 > end { return Result.Err(ParseErr.BadLen) }
    if ip.proto != 17u8 { return Result.Err(ParseErr.NotUdp) }
    if csum(pkt[l3 : l4]) != 0u16 { return Result.Err(ParseErr.BadCsum) }
    u := udp(pkt[l4 : l4 + 8])?
    if l4 + usize(u.len) != end { return Result.Err(ParseErr.BadLen) }
    return Result.Ok(Layout{ l3: l3, l4: l4, payload: l4 + 8, end: end })
}

func payload(pkt: view[]u8, l: Layout) -> view[]u8 {
    return pkt[l.payload : l.end]                            // elision: derived from pkt
}

func handle(pkt: view[]u8) -> Result[(), ParseErr] {
    l := parse(pkt)?
    port := udp(pkt[l.l4 : l.l4 + 8])?.dst_port
    with body := payload(pkt, l) {                           // view rooted at pkt for the block
        deliver(port, body)
    }
    return Result.Ok(())
}
```
`udp(...)?.dst_port` is a `Copy` field read of a returned value in one
statement; no access survives it.

## Benchmark notes
- C: the same checks over `const uint8_t *` with explicit length arithmetic
  and `memcpy`-free header decoding (`ntohs` on unaligned loads).
- Rust: slices with `get(..)?` for every bound (so the checked semantics match)
  and `u16::from_be_bytes` decoding; a `Parser<'a>` struct variant as the
  reference-holding comparison row.
- Measure: packets per second on 64-byte and 1500-byte frames from a
  preloaded ring, p50/p99 per packet, bounds checks per packet from
  `show-bounds-checks` (target: the four sub-slices in `parse` plus one in
  `payload`, with all element checks inside `csum` proven by `loop_bound`),
  copies (zero: `show-copies` must report only the 14-, 20- and 8-byte header
  structs), allocations (zero), unsafe lines (zero), text bytes, compile time,
  and the Phase 3 SIMD row for `csum`.
- Failure paths: every `ParseErr` variant has a malformed-packet fixture;
  a truncated frame must not read past `len(pkt)` (the bounds trap would be a
  bug here, so each read is guarded by a length test first).

## Open risks
- Each re-slice is a bounds check; without `dominated_by_check` the parser
  pays about seven checks per packet versus zero in C. Measure before judging.
- A stateful streaming parser (TCP reassembly) would need to retain partial
  input; that is owned bytes in a `Vec[u8]`, not a view, and costs a copy of
  the fragment. Out of scope for this workload but relevant to the §5.6
  decision.
