# Runtime, phase 8: networking

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area H (RH1 to
RH4). **Plan:** `specs/runtime/PLAN.md`, phase 8. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container; both
have an IPv6 loopback, so the IPv6 paths ran on both.

## What changed

- **`std/net`, a new module.**
  - *Addresses (RH1):* `IpAddr` (`V4`, `V6`), `SocketAddr`, `parse_addr`
    and `format_addr` for `a.b.c.d:port` and `[v6]:port`, `parse_ip` and
    `format_ip`, and `resolve` through the system resolver. IPv6 text is
    read in every form of RFC 4291 (`::`, an IPv4 tail) and written in the
    canonical form of RFC 5952 (lowercase, no leading zeros, the longest
    run of zero groups as `::`). The parsing and formatting are kern code.
  - *TCP (RH2):* `TcpListener` (`bind`, `accept`, `accept_timeout`,
    `local_addr`) and `TcpStream` (`connect`, `connect_timeout`, `read`,
    `read_timeout`, `write`, `write_all`, `shutdown`, `set_nodelay`,
    `peer_addr`, `local_addr`). A stream is an `io.Reader` and an
    `io.Writer`, so `BufReader`, `read_to_end` and `write_to` work on it.
  - *UDP (RH3):* `UdpSocket` (`bind`, `send_to`, `recv_from`,
    `recv_from_timeout`, `local_addr`).
  - Destructors close the socket.
- **`rt_net.c`, a new runtime unit of thirteen entries.**
  - Every socket is non-blocking inside the runtime, and every operation
    that can wait is a deadline loop: make the call; when it would block,
    `poll` with the time that is left; when a signal interrupts either, go
    round again with the time recomputed; when the time has passed, return
    the timed-out value. A call never blocks past its duration on
    readiness that went stale, and no socket has a mode to restore.
  - `connect` is one call; when the connection is still being made it
    waits for writability, and `SO_ERROR` is the result. On a timeout or
    a failure the descriptor, which the caller never saw, is closed.
  - Sockets are close-on-exec and above 2. A write to a closed peer is an
    error and never a signal (`MSG_NOSIGNAL` on Linux, `SO_NOSIGPIPE` on
    macOS). An IPv6 socket is IPv6 only on every host.
  - An address crosses the interface as 19 bytes: the family, 16 bytes of
    address, the port high byte first.
- **The interpreter** makes the same calls through Go's network poller,
  whose read, write and accept deadlines stand for the runtime's loop. It
  turns off what Go turns on by default and the system does not
  (`TCP_NODELAY`, keep-alive), and sets the listen queue the program
  asked for.

## Language change

- **`match f()? { Some(v) => ... }` owns its payload.** The value `?`
  unwraps from a temporary is a temporary; the arm's binding was an
  access, so `accept_timeout(d)?` could not hand its stream on
  (`tests/run/match_try_owned.kern`, `docs/ownership.md`).

## Requirement to test

| Clause | Test |
|---|---|
| RH1: `parse_addr`, `format_addr`: 16 IPv4 texts and 31 IPv6 texts, valid and not; the canonical text parses back | `tests/run/net_addrs.kern`, both engines |
| RH1: `parse_ip`, `format_ip` | same |
| RH1: `resolve` of `localhost` (a loopback address, with the port), of an address as text, of an empty name and of one with a NUL byte (`InvalidInput`) | same |
| RH2: bind to port 0 and `local_addr`; `accept`; `connect`; `peer_addr`, `local_addr`; `set_nodelay`; `write_all`; `shutdown(Write)`; `read` to the end and after it | `tests/concurrency/net_tcp_echo.kern` (a server thread and a client, also under the thread sanitizer) |
| RH2: `Reader` and `Writer`: `write_to`, `flush`, `BufReader.read_line`, `read_to_end` over a stream | same |
| RH2: a megabyte through one connection | same |
| RH2: IPv6 | same (`v6`), `tests/run/net_udp.kern` (`six`) |
| RH2: `accept_timeout` gives up after its duration (and not before); a duration of zero looks once; a pending connection is accepted | `tests/run/net_timeouts.kern` (`waits`) |
| RH2: `read_timeout` likewise; the socket reads and writes after a timeout; an empty buffer | same (`talk`) |
| RH2: `connect` and `connect_timeout` to a closed port are `ConnectionRefused`, not a timeout | same (`refused`) |
| RH2: `connect_timeout` that gives up returns `None` and leaves no descriptor: with a duration of zero, and to a listener whose queue is full | same (`given_up`) for `None`; for the descriptors, `TestConnectsThatFailLeaveNoDescriptor`: the count of `/dev/fd` before and after in a process of the program's own, in both engines, with attempts that time out and that are refused added |
| RH2, RH3: destructors close | `TestConnectsThatFailLeaveNoDescriptor` (the count after every listener and connection is destroyed) |
| RH3: `send_to`, `recv_from` with the sender's address, an answer to it; a datagram longer than the buffer is cut and its rest lost; an empty datagram; 8,000 bytes; a socket to itself | `tests/run/net_udp.kern` |
| RH3: `recv_from_timeout` gives up and the socket goes on working; zero looks once | same |
| RH4: a read after the peer closed returns 0; a write is `BrokenPipe` or `ConnectionReset` and the program goes on | `net_timeouts.kern` (`talk`) |
| RH4: close-on-exec, above 2 | `TestRuntimeABI` compiles the unit; the socket path shares `F_DUPFD_CLOEXEC` and `O_CLOEXEC`'s equivalents with files, whose inheritance tests are `TestProcInheritFile` |
| RH4: the three types are `Send` | `tests/concurrency/net_sockets_move.kern` (each handed to another thread) |
| RH4: loopback only | every test above |

## Deviations and open points

- **Addresses are passed by value**, not by `view`: they copy, and a call
  can then take another call's result (`bind(net.loopback_v4(0u16))`).
- **`recv_from` returns `Datagram { len, from }`**, not a tuple: kern has
  tuples only for multi-access returns.
- **Additions to the spec's list:** `TcpListener.bind_with_backlog`,
  `parse_ip`, `format_ip`, and the constructors `v4`, `addr`,
  `loopback_v4`, `loopback_v6`, `any_v4`, `any_v6`.
- **A full listen queue.** The plan expected a connect to a full queue to
  stay pending on the loopback. It does on Linux; macOS turns it away
  with a reset. The test accepts either, and the zero-duration connect
  exercises the give-up path on both.
- **A duration of zero in the interpreter** is a millisecond for accept,
  read and receive, since Go's poller refuses a call whose deadline has
  passed without trying it; for connect it is an attempt given up at
  once, as in the runtime.
- **No scope identifier**: an IPv6 link-local address cannot name its
  interface.
- **A signal interrupting a blocked call** is retried by the loop; no
  test provokes it until phase 9 brings signal handlers.
- **No socket has its own inheritance test**; a socket descriptor is made
  close-on-exec by the same calls as a file's.

## Verification

`go test ./...` passes on macOS, the socket fixtures under the thread
sanitizer included; the phase's fixtures, the area-A checks and the
sanitized corpus pass in the Linux container; `golangci-lint run ./...`
reports no issue. The echo and timeout fixtures were each repeated natively
without a differing run.
