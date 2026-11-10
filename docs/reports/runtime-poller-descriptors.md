# Runtime, phase 14: readiness, descriptors, credentials, abstract names

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.9, area N (RN1 to
RN4). **Plan:** `specs/runtime/PLAN.md`, phase 14. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

Phase 13 had written a non-goal into the spec that no requirement had
ever stated and the owner had not approved: nothing that one host alone
has, and no passing of descriptors or credentials. This phase withdraws
it and builds what it excluded. It also adds the one thing the standing
non-goal for an asynchronous scheduler left a program without: a way for
one thread to wait for many sockets.

## What changed

- **`net.Poller` (RN1).** The system's readiness queue as a value: epoll
  on Linux, kqueue on macOS, three runtime entries. A program watches a
  socket under a token of its own, for reading, writing or both; `wait`
  and `wait_timeout` return the sockets that can be read or written
  without waiting; the program then makes the ordinary calls.
  - Reports are level-triggered on both hosts and the same in shape: a
    socket ready both ways is one report (kqueue's two events are merged,
    as epoll gives one), and a peer's close is `readable` and `closed`.
  - Watching a socket again replaces its token and what is asked, on both
    hosts: epoll's modify-then-add and kqueue's add-and-delete are behind
    one entry.
  - It is not a scheduler. Nothing suspends, no code of the program runs
    but on the thread that calls `wait`, and ADR-017's decision stands
    with a paragraph added. An event loop is a `while` in the program;
    to wake it from another thread, watch one end of
    `UnixStream.pair()`.
  - Every socket type implements the interface `Source`, which is what
    `watch` and `send_with` take.
- **Descriptors over a Unix-domain stream (RN2).** `send_with` and
  `send_file` attach an open socket or file to at least one byte;
  `recv_with` returns `Bytes(n)` or `WithDescriptor(n, d)`. A
  `Descriptor` says what the system says it is (`kind`) and becomes the
  type the program names (`into_file`, `into_tcp_stream`, five more) or
  fails with `InvalidInput`.
  - A received descriptor is close-on-exec from the moment it exists
    (`MSG_CMSG_CLOEXEC` on Linux). Descriptors after the first in a
    message, and those that come with bytes an ordinary read takes or
    with a datagram, are closed, so a peer cannot fill the program's
    descriptor table. As first written this held on Linux only, where
    the system closes what a receive has no room for. macOS opens every
    descriptor sent in the receiving process before it looks at the
    room, and reports those that fit: a plain `read` left one descriptor
    open per message, with no number the program ever saw, and a message
    with twelve left four. It was found while a descriptor count that
    did not balance on GitHub's macOS host was looked into (see below).
    Every receive on a Unix-domain socket now gives room for the most one
    message can carry (253 descriptors on Linux, 254 on macOS) and
    closes what its caller does not take, in both engines;
    `TestDescriptorsNotTakenAreClosed` has a program that is not kern
    send twelve, three and two and counts.
  - macOS defines the socket option that tells a listener from a stream
    and refuses to read it. The runtime reads the same bit from the
    process's descriptor table there (`proc_pidfdinfo`), and the
    interpreter makes the same query.
- **Peer credentials (RN3).** `UnixStream.peer_credentials()`: the user,
  group and process the system recorded for the other end (`SO_PEERCRED`
  on Linux; `getpeereid` and `LOCAL_PEERPID` on macOS).
- **Abstract names (RN4).** A Unix-domain path whose first byte is zero
  is a name in Linux's abstract namespace, in every call that takes a
  path and in a datagram's reported sender (which the entry now returns
  as a length and bytes, since such a name begins with a zero). On macOS
  the same call is `Unsupported` before any system call.

## What the work found in the compiler

The descriptor fixture's `sender.send_to("datagram", u.local_addr()?)`
did not compile: an internal error, `access to %sender conflicts with
open access`. Two defects in the lowering of `?` stood behind it.

| Finding | Fix |
|---|---|
| A call opens the accesses of its `view` and `edit` arguments as it evaluates them; a `?` in a later argument returned with those accesses open, and the destruction of a local they reached into was then invalid IR | the error path of `?` ends the accesses that enclosing calls have opened, newest first |
| On the same path the owned operands already evaluated (an owned argument, a temporary receiver, the earlier fields of a struct literal, the earlier payloads of a variant, the earlier elements of an array, an argument moved out of a local) were never destroyed: Rule 5.7.2 was not kept, and a value with a destructor leaked | the operands of a call or a literal in progress are held until it takes them, and the error path of `?` destroys those held, newest first |

`tests/run/try_in_arguments.kern` has each case with a destructor that
prints, on both paths, in all four configurations; `docs/ownership.md`
states the rule.

## Review

The diff went to an internal model (prism's default; 28 findings). Four
led to a change: a retry loop in the Poller fixture that counted tries
where it should have counted time (the byte it waits for may take a
moment to arrive on a loaded host); the length byte of a datagram's
sender, now bounded by the buffer that holds it; a stale comment; and a
note where the two-phase accesses begin, saying why none is ever pending
at a `?`. The run of the whole suite found what the review had not: on
macOS a datagram's unbound sender was read as an abstract name, because
the system reports such a sender with a path of zero bytes; the entry
now reads abstract names on Linux only.

The others were declined: the interface version stays 2, this unreleased
work's number; a control message cut short loses only descriptors the
entry would have closed; a failed `into_` closes nothing twice (the entry
leaves the descriptor open and the `Descriptor` closes it); the held
operands are owned by construction, since a value of a type that is not
`Copy` reaches an argument only by a move; and the structure offsets the
interpreter uses on macOS are checked by the fixture that names a
listener a listener.

## Requirement to test

| Clause | Test |
|---|---|
| RN1: nothing is ready before anyone connects; a timed wait takes its time; a look returns at once; neither direction is `InvalidInput` | `tests/run/net_poller.kern` |
| RN1: a listener is readable when a connection waits, under its token; a stream when bytes do; unread bytes are reported again; once read, nothing is | same |
| RN1: one thread accepts three connections, echoes each and reads the echoes as their clients | same |
| RN1: watching again replaces the token and what is asked; readable and writable are one report | same |
| RN1: a peer's close is `readable` and `closed`, and a read returns the end; `unwatch`, and `NotFound` for what is not watched; a closed socket is not reported | same |
| RN1: a Unix-domain pair and a UDP socket are watched as TCP sockets are | same |
| RN1: with a hundred sockets ready and none read, no wait reports more than 64 and every one comes round again within sixteen waits | `tests/run/net_poller_many.kern` (added with the final gates) |
| RN1: a reset connection is `readable` and `failed`, and a read returns `ConnectionReset` | `tests/run/net_poller.kern` (added with the final gates) |
| RN1: a Poller on its own thread serves four client threads (400 messages, 6,400 bytes) and is stopped through a pair | `tests/concurrency/net_poller_threads.kern` (also under the thread sanitizer) |
| RN2: a file crosses a pair, reads on from the sender's offset, and moves the sender's offset | `tests/run/net_descriptors.kern` |
| RN2: a TCP listener, a TCP stream, a UDP socket, a Unix-domain stream, listener and datagram socket each cross, are named rightly by `kind`, become their type and work | same |
| RN2: a file as a TCP stream and a datagram socket as a stream are `InvalidInput` | same |
| RN2: bytes alone bring no descriptor; no bytes, or no buffer, is `InvalidInput`; a timed receive gives `None`; an ordinary read takes the byte of a message that carried a descriptor | same |
| RN2: files are exchanged with a process that is not a kern program | `TestDescriptorsBetweenProcesses`, both engines |
| RN3: both ends of a pair see the same user, group and process | `tests/run/net_descriptors.kern` |
| RN3: the credentials a program reads are the peer's real user, group and process | `TestDescriptorsBetweenProcesses`, both engines |
| RN4: on Linux a listener and a connection, a second bind (`AddrInUse`), a bind after the close, and two datagram sockets whose receiver sees the sender's abstract name | `TestAbstractSocketNames`, both engines, named in the Linux gate |
| RN4: on macOS the same calls are `Unsupported`; a name of no bytes or holding a zero byte is `InvalidInput` on both | same |
| Rule 5.7.2 for the operands of a call or literal that a `?` leaves | `tests/run/try_in_arguments.kern` |

## Deviations

- **A wait reports at most 64 sockets**; the rest come with the next
  wait, which level-triggering guarantees.
- **`Received` is a union**, `Bytes(n)` or `WithDescriptor(n, d)`: a
  `match` takes the descriptor out, which it could not from a field.
- **In the interpreter on Linux** a file's relative path that begins
  with `@` is bound as `./@...`, because its host's network package
  reads a leading `@` as an abstract name.

## After the first push

The work was first run on GitHub's hosts when `main` was pushed at
327b07d. Both Linux hosts and the sanitized corpus passed. The macOS
host (macOS 26; this work was done on macOS 27) failed one line of one
fixture, twice in two runs: `tests/run/net_timeouts.kern`, run by the
interpreter inside the test binary, counted the process's descriptors
around twelve connection attempts to a full queue and the count did not
balance.

- **What it led to.** Looking for a descriptor that outlives its owner
  found the one described above: on macOS a descriptor sent over a
  Unix-domain socket and not taken stayed open in the receiver. That is
  fixed and tested (`TestDescriptorsNotTakenAreClosed`), and is not what
  the fixture counted.
- **What the fixture counted is not reproduced here.** On macOS 27, with
  the Go release the hosts use, the fixture balances in every run, in
  both engines, and so does a program that forces attempts to time out,
  to be refused and to be turned away. What differs on macOS 27 is the
  system: an attempt to a full queue is reset at once, where the
  fixture's time on the macOS 26 host says attempts waited there. The
  count is of the whole process, and the test binary is not the
  program's own process: the system's name resolver, which an earlier
  fixture uses, leaves two sockets of its own open in it (seen here),
  and may close them at a time of its own (not seen here). A count over
  a second of waiting is exposed to that where a count over a
  millisecond is not, which fits the line that failed; it is a reading
  of the evidence, not something observed.
- **What changed.** A count of a process's descriptors proves something
  only in a process of the program's own, so that is where it is now
  made. The fixture keeps what the attempts returned;
  `TestConnectsThatFailLeaveNoDescriptor` runs the program by itself, in
  both engines, with attempts given up at once, turned away by a full
  queue, timed out and refused, counts after each kind and again after
  every listener and connection is destroyed, and prints both numbers
  when a count is off. With the runtime's close of a timed-out attempt
  removed, it reports the descriptors left. Should the interpreter leak
  on that host after all, this test says so there, with the numbers.

With these changes `go test ./...` and the sanitized corpus pass on
macOS 27, and the Linux container's gate passes with 2,816 tests, the
two new tests named in it, at the fix and again after the count was
moved out of the fixture. The macOS 26 host has not run them yet.

## Verification

`go test ./...` passes on macOS, with every freestanding image booted
under QEMU on both architectures and the concurrency fixtures marked for
it run under the thread sanitizer, and so does the sanitized corpus. In
the Linux container the whole suite with the sanitized corpus passes:
2,807 tests, where phase 13 ended with 2,789. The new fixtures and tests
are named in the gate, so one that is skipped there fails it; among them
`TestAbstractSocketNames`, which on Linux is the test of the names
working. `golangci-lint run ./...` reports no issue, `gofmt -l` lists
nothing, and `std/*.kern` and their prelude copies are identical.
