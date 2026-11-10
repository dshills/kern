# Runtime, phase 12: extensions

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.7, area L (RL1 to
RL6). **Plan:** `specs/runtime/PLAN.md`, phase 12. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container, which
now has QEMU.

The first eleven phases ended with a list of what a user would miss. Two
entries on it are decisions (no asynchronous I/O, no TLS: spec §2 and
ADR-017). The others were gaps, and this phase closes them.

## What changed

- **Unix-domain sockets (RL1).** `net.UnixListener` (`bind`,
  `bind_with_backlog`, `accept`, `accept_timeout`) and `net.UnixStream`
  (`connect`, `connect_timeout`, `pair`, `read`, `read_timeout`, `write`,
  `write_all`, `shutdown`), an `io.Reader` and `io.Writer` like
  `TcpStream`. Two runtime entries make the socket; reading, writing,
  accepting and closing are the entries TCP uses.
  - A path of 104 bytes or more, an empty one, or one with a NUL byte is
    `InvalidInput` before any system call: 104 is the smaller of the two
    hosts' limits, so a path is valid on both or on neither.
  - A listener does not remove its path, and `bind` fails with
    `AddrInUse` when the path exists: the program removes it.
  - A connect to a listener whose queue is full is made again every
    millisecond until its time has passed; the system does not finish
    such a connect in the background as it does for TCP.
- **Directory trees (RL2).** `io.read_dir` (names with kinds, a symbolic
  link reported as one), `io.create_dir_all`, `io.remove_dir_all` (which
  removes a link and never follows one), `io.join`. All kern code over
  entries that were there.
- **File locks (RL3).** `File.lock`, `try_lock` and `unlock`: `flock`,
  advisory, belonging to the open file.
- **Mapped files (RL4).** `mem.map_file(file, writable)` gives a
  `Mapping`: `bytes`, `bytes_mut`, `sync`, unmapped when destroyed. The
  mapping is shared, so a write through it is a write to the file, and it
  outlives the `File` it was made from.
- **The freestanding stack and start-up code (RL5).** A freestanding
  build's `--stack-bytes=N` is its stack's size (65,536 by default), and
  `--startup=<file.S>` compiles start-up code of the user's in place of
  the architecture's. Both are keys of `kern.mod`.
- **The Linux gate boots images (RL6).** The container has
  `qemu-system-arm` and `qemu-system-x86` and runs with
  `KERN_REQUIRE_QEMU=1`: every freestanding fixture and board test boots
  there on both architectures, and a missing emulator fails the gate.

## Requirement to test

| Clause | Test |
|---|---|
| RL1: bind, accept, connect, echo by lines through the interfaces, `shutdown` | `tests/concurrency/net_unix.kern` (a server thread and a client; also under the thread sanitizer) |
| RL1: `accept_timeout`, `read_timeout` | same |
| RL1: a second bind is `AddrInUse`; the path stays when the listener is gone, a connect to it is `ConnectionRefused`, to a removed path `NotFound`; bind works again after removal | same |
| RL1: `pair`: both ends read and write; a read after the other end is gone returns 0 | same (`pair`) |
| RL1: an empty path, 104 bytes, a NUL byte are `InvalidInput` | same (`refusals`) |
| RL2: `create_dir_all` makes missing parents, succeeds on an existing directory, ignores slashes at the end; over a file `AlreadyExists`, under a file `NotADirectory`, of nothing `InvalidInput` | `tests/run/io_tree.kern` |
| RL2: `read_dir`: names in order with kinds, a link as a link; of a missing directory, of a file | same |
| RL2: `remove_dir_all` removes a tree, refuses a file, a missing path and a link to a directory, and leaves what a link in the tree points to | same |
| RL3: `try_lock` is false against an exclusive lock, shared or exclusive; shared locks coexist | `tests/concurrency/io_lock.kern` |
| RL3: `lock` waits until the holder lets go | same (a thread that has the lock only after `unlock`) |
| RL3: closing the file ends its lock | same |
| RL4: a mapping's bytes are the file's; a writable mapping changes the file at `sync` and when destroyed, also after its `File` was closed | `tests/run/mem_map_file.kern` |
| RL4: an empty file gives an empty mapping | same |
| RL4: a writable mapping of a file opened for reading, and any mapping of a file opened for writing only, are `PermissionDenied` | same |
| RL4: `bytes_mut` of a mapping that is not writable traps `explicit` | `tests/trap/mem_map_readonly_mut.kern` and twin |
| RL5: the default stack ends a recursion of some 300 KiB in a `stack` trap; a megabyte carries it through | `TestFreestandingStackBytes`, both architectures under QEMU |
| RL5: `--startup`: start-up code with one more call boots and makes it | `TestStartupFile`; a missing file is refused there and by `TestBuildFreestandingOptions/refusals` |
| RL6: images boot in the container | every `tests/freestanding` fixture and every board test, run there with `KERN_REQUIRE_QEMU=1` |

## Deviations and open points

- **`UnixPair` is a union of one variant**, `Ends(a, b)`, not a struct:
  kern cannot move two fields out of a struct, and a `match` moves both
  payloads out.
- **In the interpreter a mapping is a copy** of the file's bytes, written
  back at `sync` and when the mapping is destroyed. A program that writes
  through a mapping and reads the file before either sees the old bytes
  there, and another program's writes are not seen.
- **Locks are `flock`'s**: they bind programs that ask, hold per open
  file, and say nothing about byte ranges.
- **No datagram or abstract Unix-domain sockets**, and no passing of
  descriptors or credentials over one.

## Verification

`go test ./...` passes on macOS and, whole, in the Linux container, where
every freestanding image now boots; the sanitized corpus passes on both;
`golangci-lint run ./...` reports no issue.
