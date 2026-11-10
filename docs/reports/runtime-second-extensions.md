# Runtime, phase 13: second extensions

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.8, area M (RM1 to
RM6). **Plan:** `specs/runtime/PLAN.md`, phase 13. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

Phase 12's report ended with what `std/io` and `std/net` did not reach
(locks on ranges, datagrams between programs on one machine, scoped
addresses, socket options) and two ways the interpreter differed from
the hosted runtime (a mapped file was a copy; a signal at its default did
what the interpreter's host does, not what the system does). This phase
closes all six.

## What changed

- **Locks on ranges of a file (RM1).** `File.lock_range(start, n,
  exclusive)`, `try_lock_range` and `unlock_range`. One runtime entry,
  `kern_io_lock_range`, over the locks of an open file description
  (`F_OFD_SETLK`, `F_OFD_SETLKW`), which both hosts have.
  - They were chosen over the process's record locks because they behave
    as `File.lock` does: the lock is the open file's, two opens in one
    process contend, and closing some other open of the file releases
    nothing. A process's record locks are lost when any descriptor of
    the file is closed, by any part of the program.
  - `n` of 0 reaches to the end of the file however it grows. The system
    names a conflict `EAGAIN` or `EACCES`; the entry returns one.
  - A shared lock needs the file open for reading and an exclusive lock
    for writing. The system's error for the other case is the one it has
    for a bad descriptor; the entry returns `InvalidInput`.
  - On macOS a range lock and a whole-file lock on one file conflict; on
    Linux they do not see each other. The reference says to guard a file
    with one kind.
- **IPv6 scopes (RM2).** `SocketAddr` has a third field, `scope`: the
  index of the interface a link-local address is on. The runtime's
  record of an address grew from 19 bytes to 23, and every entry that
  takes or returns one carries the scope both ways. `parse_addr` reads
  `[fe80::1%en0]:80` and `[fe80::1%4]:80`; `format_addr` writes the
  index, so its text does not depend on the machine's names.
  `interface_index` and `interface_name` are two new entries over
  `if_nametoindex` and `if_indextoname`.
- **Unix-domain datagrams (RM3).** `net.UnixDatagram`: `bind`, `unbound`,
  `send_to`, `recv_from`, `recv_from_timeout`; `UnixMessage` carries the
  sender's path, empty for a socket that is not bound. Three entries.
  - A datagram here is not lost on the way, so `send_to` waits for a
    receiver whose queue is full. The socket's own readiness does not
    show that state (macOS answers `ENOBUFS`, Linux `EAGAIN` on a socket
    that polls as writable), so the entry sends again each millisecond.
- **Socket options (RM4).** Two entries, `kern_net_set_opt` and
  `kern_net_get_opt`, take an option by a number of kern's own; the
  methods of `std/net` name them: keep-alive, no-delay, the two buffers,
  the hop limit and linger on a `TcpStream`; broadcast, the buffers and
  the hop limit on a `UdpSocket`; the buffers on the Unix-domain sockets.
  - The hop limit is IPv4's TTL or IPv6's hop limit, chosen by the
    socket's family. Linger is in seconds: macOS's `SO_LINGER` counts in
    ticks, so the runtime uses `SO_LINGER_SEC` there.
  - `kern_net_get_opt` returns `-errno - 1` on failure, since linger that
    is off is the value -1.
- **The interpreter maps files (RM5).** `mem.map_file` in the interpreter
  is now `mmap` with `MAP_SHARED`, as in the runtime. The memory gets an
  address of the interpreter's own above its heap, and leaves that
  address space before it is unmapped, so an access after `release` is a
  trap. A mapping made for reading is mapped for reading: a write to it
  in `unsafe` code traps `unreachable`, the kind the runtime reports for
  the system's fault. A fault the system does raise (a byte past the end
  of a file cut short) reaches the interpreter as a recoverable error of
  its host, which it reports as a trap of kind `unreachable`.
- **The interpreter's signal defaults (RM6).** Under `kern run` the
  interpreter asks for the signals its host would handle otherwise
  (`User1`, `User2`, `Quit`), and one the program neither catches nor
  ignores ends the process by that signal: the system's default is
  restored with one `sigaction` call and the signal is sent again.
  `signal.raise` of a signal at its default ends the program before it
  returns, in both engines, and both flush standard output first.
  `Interrupt`, `Terminate` and `Hangup` already ended an interpreted
  program by the signal.

## What the work found

| Finding | Fix |
|---|---|
| The interpreter's UDP sockets could send to a broadcast address without `set_broadcast`: its host's network package turns that option on for every datagram socket | the option is cleared when the socket is made; `net_options.kern` reads it as off in both engines |
| A native program that raised a fatal signal on itself lost what it had written to standard output and not yet flushed, and an interpreted one did not | `kern_signal_raise` flushes first |
| A lock refused because the file is not open the right way came back as the error of a bad descriptor, kind `Other` | `InvalidInput`, in both engines |
| The Linux container had no IPv6 link-local address, so the test of a scoped address on a real link was skipped there without a word | `scripts/linux-check.sh` runs the container on a network with IPv6; the test waits out the second or two in which the system checks a new address |

## Review

The diff of the runtime, the interpreter and the library went to an
internal model (prism's default, twenty findings). Seven led to a change:

| Finding | Change |
|---|---|
| Between taking a signal's subscription away and asking for it again, an interpreted program would let that signal pass | a signal whose default the interpreter supplies is asked for once and for good; ignoring it is a bit its reader consults |
| `addr_scoped` kept a scope on an IPv4 address that the record then dropped | it drops it itself, and the fixture shows the value |
| `set_hop_limit` left the range to each system | 0 and numbers above 255 are `InvalidInput` before the call, on every system |
| A sender's path longer than 104 bytes is cut without a word | the entry's contract and `UnixMessage` say so |
| A zone of digits is never looked up as a name | `parse_scope` says so |
| `dieBySignal` waited a second when the signal could not be sent | it waits only for a signal that was sent |
| A comment counted four signals where the list has three | the comment says why `Pipe` is not in the list |

The others were declined: `F_OFD_SETLK` exists on macOS (the fixture runs
there); the fault-to-trap switch is per thread of the interpreter, not
per process; the mapping's record is freed by `mem_unmap`, which the
finding did not follow; the system refuses a mapping the file's mode does
not allow, as natively; and the interface version stays 2, which is this
unreleased work's number: the record of 19 bytes was never in a release.

## Requirement to test

| Clause | Test |
|---|---|
| RM1: ranges that overlap conflict, ranges that do not coexist; shared locks coexist; the holder may lock its own range again | `tests/concurrency/io_lock_range.kern` (all four configurations and the thread sanitizer) |
| RM1: unlocking part of a range leaves the rest held | same |
| RM1: `n` of 0 reaches every later byte; bytes beyond the file's end can be locked | same |
| RM1: `lock_range` waits until the holder lets go | same (a thread that has the range only after `unlock_range`) |
| RM1: closing a file ends its locks, and only its own | same |
| RM1: a shared lock on a file open for writing only, an exclusive one on a file open for reading: `InvalidInput` | same |
| RM2: scoped text read and written; an empty zone, zero, a leading zero, a number out of range, an unknown name, two zones and a zone on an IPv4 address are refused | `tests/run/net_scopes.kern` |
| RM2: the record's last four bytes; an IPv4 address carries no scope | same |
| RM2: interface 1 by index and by name; unknown names and indexes | same |
| RM2: a socket bound to a link-local address reports its scope, and a datagram's sender carries it, with the zone written as a name and as an index | `TestScopedAddressOnALink`, both engines: over `lo0` on macOS and over `eth0` in the Linux container, whose network now has IPv6 for this (the test is named in the gate, so a skip there fails it) |
| RM3: two bound sockets exchange datagrams and each sees the other's path; an unbound sender has none | `tests/concurrency/net_unix_dgram.kern` |
| RM3: an empty datagram; a datagram cut to the buffer, the rest lost | same |
| RM3: a timed receive waits its whole time, a zero time looks once, a waiting datagram is received | same |
| RM3: 3,000 datagrams to a receiver that starts 100 ms late: all received, in order | same |
| RM3: a bound path is `AddrInUse`; an empty or too long path `InvalidInput`; a path nothing is at `NotFound`; a closed socket's path `ConnectionRefused` | same |
| RM4: each option off (or the system's size) on a new socket, set, read back, cleared | `tests/run/net_options.kern`: TCP over IPv4, UDP over both families, a Unix-domain pair |
| RM4: a hop limit of 256 or of 0 is `InvalidInput` and changes nothing; linger of 3 s, 0 s and off | same |
| RM4: an accepted connection has its own options | same |
| RM5: a write through one mapping is seen through another and by a read, with no sync; a write to the file, by the same open and by another, is seen through both mappings | `tests/run/mem_map_shared.kern` |
| RM5: RL4's fixture is unchanged | `tests/run/mem_map_file.kern` |
| RM5: a mapped file cut short, and a write in `unsafe` code through a mapping made for reading, are one `unreachable` trap record in both engines, after the same output | `TestMappedFileFaultsAreTraps` |
| RM6: `Terminate`, `User1`, `Quit`, `Pipe` and `Hangup` raised at their default, and `User2` ignored, raised, reset and raised again: death by that signal after the same output, natively and interpreted; `Child` does nothing | `TestSignalDefaultsEndTheProgram` |
| RM6: `User1` sent from outside is caught while caught and ends the program once it is not | `TestSignalDefaultFromOutside`, both engines |
| RM6: a program run in-process leaves the process's signals alone | every fixture of `std/signal`, which the test suite runs inside its own process |

## Deviations

- **The interpreter's record of a fault has the span 0-0.** The runtime
  names the function the fault happened in when its walk finds one.
- **A `Pipe` signal another process sends to an interpreted program is
  not seen**: the interpreter's host keeps it, whatever was asked. One
  the program raises is, and a write to a closed pipe or socket is an
  error in both engines.
- **Should the interpreter's `sigaction` call fail**, it exits with 128
  plus the signal's number, the status a shell reports for a program a
  signal ended.

## Verification

`go test ./...` passes on macOS, with every freestanding image booted
under QEMU on both architectures and the concurrency fixtures marked for
it run under the thread sanitizer, and so does the sanitized corpus. In
the Linux container the whole suite with the sanitized corpus passes:
2,789 tests, where phase 12 ended with 2,758. The new fixtures and tests
are named in the gate, `TestScopedAddressOnALink` among them, so one that
is skipped there fails it. `golangci-lint run ./...` reports no issue,
`gofmt -l` lists nothing, and `std/*.kern` and their prelude copies are
identical.
