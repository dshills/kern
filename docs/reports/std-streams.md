# Standard library: line buffers, prefix removal, timed writes, signals in a Poller

**Date:** 2026-11-11. **Spec:** `specs/std-streams/SPEC.md` v0.3.
**Plan:** `specs/std-streams/PLAN.md`, five phases. **Hosts:** macOS
27.2 on arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check`
container.

Five applications were written on the runtime on 2026-11-10
(`examples/`). Four things had to be written by hand in them, some more
than once, or could not be written at all. This work adds them to the
library and rewrites two of the applications on them.

## What changed

- **`io.LineBuf`.** A buffer that splits bytes into lines and does not
  own where they come from. `io.BufReader` takes its stream, so a
  program that reads lines from a socket cannot write to it, and a
  `Poller` hands a server its bytes in pieces that a `BufReader` has no
  way to accept. `feed` takes bytes; `next_line` hands back a whole
  line (ended by `\n` or `\r\n`) or says there is none; `take_rest`
  hands over what is left; `pending` says how much is held; `read_line`
  does the feeding from any `Reader`, reading only when no whole line is
  held. Three of the five applications had their own copy of this loop.
  - It searches a byte for a newline once: an index remembers how far
    it has looked, so a long line that arrives a byte at a time costs
    its length, not its length squared. It moves a byte once per
    halving: the consumed front is dropped when it is at least 4,096
    bytes and at least half the buffer.
  - The fixture measures this against a single feed of the same run
    (R6). In the interpreter a line of 65,536 bytes fed whole took 20
    ms, fed a byte at a time 106 ms, and 100,000 short lines 502 ms;
    natively 27 µs, 239 µs and 643 µs.
- **`Vec.remove_prefix(n)`.** Destroys the first `n` elements and moves
  the rest to the front with one copy. A server that has sent part of
  what it owes a client had to build a second vector of the remainder.
- **`TcpStream.write_timeout` and `UnixStream.write_timeout`.** One
  runtime entry, `kern_net_send_timeout`. The sockets were non-blocking
  inside the runtime already, and a write waited in `poll` without a
  limit; this entry gives the wait the time that is left, and with no
  duration it offers the bytes once. In the interpreter Go's `Write`
  would go on until every byte was sent, so the bytes are offered to
  the descriptor itself, through `SyscallConn`, and Go's write deadline
  does the waiting between offers.
- **A `Signals` in a `Poller`.** One runtime entry, `kern_signal_fd`,
  the read end of the pipe the signal handler writes a byte to, and
  `impl Source for signal.Signals` in `std/net`. Two things had to
  change for the pipe to be readable exactly while a signal waits:
  - `kern_signal_wait` returned a recorded signal without reading the
    pipe, and gave up, when asked not to wait, before it looked at the
    pipe at all. A `Poller` is level-triggered, so the byte would have
    been reported for ever. The wait now empties the pipe whenever it
    finds nothing recorded, and looks at the records once more, for one
    made before the pipe was emptied.
  - The interpreter kept its signals in a channel. It now has the pipe
    too, written under the lock that guards the records.
- **`str.ends_with`.**

## The examples

- `kvstore` holds a `LineBuf` for each connection, offers its replies
  with a write that never waits, keeps what a socket did not take and
  watches that socket for writing, stops reading a client that is owed
  more than a megabyte, and watches its `Signals` in the same `Poller`.
  Its loop has no timed wake left, and the note at its head that a
  client which stopped reading would hold the server is gone with the
  defect.
  - Run as a server, with a client that sent 20,000 requests for a 2 KB
    value and read nothing: another client was answered in 0.5 ms, and
    the silent client then read every byte it was owed (40,140,000).
  - Its demonstration sends 5,000 commands before it reads a reply, and
    stops the server by raising `Terminate`.
- `supervise` splits its children's output with a `LineBuf`.

## Decisions

- **A buffer the program feeds, not a reader that borrows.** kern has
  no first-class references, so a line reader cannot hold the stream it
  reads without owning it. The stream is an argument of each
  `read_line` instead, and between calls the program has it to itself.
- **`write_timeout`, not a non-blocking mode.** A mode would change
  what every other call on the socket does. One call with a duration,
  whose zero means "now or not at all", is the shape `read_timeout`
  and `accept_timeout` already have.
- **The signal descriptor outlives the `Signals`.** Closing the pipe
  when the `Signals` is destroyed would race with a handler that is
  still running on another thread and about to write to a descriptor
  number the system may have given to something else. The pipe is kept
  for the life of the process, as it was, and the reference says that a
  watch is ended with `unwatch`.
- **Watching a `Signals` for writing is `InvalidInput`.** The entry
  that registers a descriptor refuses write interest in the signal
  descriptor, in both engines, rather than leave it to what each host's
  queue makes of the read end of a pipe.

## What was found on the way

- **ThreadSanitizer and a wait in the readiness queue.** The fixture
  for a signal that arrives while `Poller.wait` blocks never ended
  under the sanitizer on macOS, and passed everywhere else. The
  sanitizer runs a program's handler only when the thread that received
  the signal enters a call it intercepts, and `kevent` is not one, so
  the handler never ran and the byte was never written. That is the
  sanitizer's, not the program's: the case is a fixture of its own,
  `signal_poller_wakes.kern`, which is not run under it, and
  `signal_poller.kern`, with every other case, is.
- **One late report.** A handler sets the signal's record and then
  writes the byte. A thread that takes the record and empties the pipe
  between the two leaves a byte with no record behind it, and the
  `Poller` reports once more. R9 allows that report; the program's
  `try_next` returns `None` and the pipe is empty again.

## Tests

New fixtures, each with its expected output written from the spec
before it was run: `run/vec_remove_prefix`, `trap/vec_remove_prefix_bounds`,
`run/io_linebuf`, `concurrency/net_write_timeout`,
`concurrency/signal_poller`, `concurrency/signal_poller_wakes`; cases
added to `run/std_str`. The three existing signal fixtures and the
signal tests pass unchanged.
