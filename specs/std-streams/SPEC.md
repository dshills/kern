# Standard library: line buffers, prefix removal, timed writes, signals in a Poller

**Version:** 0.3 (2026-11-11). **Review status.** speccritic
(gpt-6.1-sol, with `specs/runtime/SPEC.md` as context) round 1:
VALID_WITH_GAPS, no critical finding; three warnings resolved here: the
two runtime entries have their prototypes and result encodings (§1),
R6's limit is relative to a measurement of the same run instead of a
number of seconds on an unnamed host, and what happens when a
destination vector cannot grow is stated (§4). Round 2:
VALID_WITH_GAPS, no critical finding; two warnings resolved: an expired
wait makes no further offer (R8), and a `Signals` exists only when the
signal pipe does (R9). **Scope:**
- the prelude (`compiler/resolve/prelude/core.kern`, mirrored identically
  in `std/core.kern`): `Vec.remove_prefix`;
- `std/io` (`compiler/resolve/prelude/io.kern`, mirrored in
  `std/io.kern`): `LineBuf`;
- `std/str`: `ends_with`;
- `std/net`: `TcpStream.write_timeout`, `UnixStream.write_timeout`, and
  `signal.Signals` as a `Source` a `Poller` can watch;
- `std/signal`: the runtime entry for the descriptor behind a
  `Signals`;
- two new runtime entries, `kern_net_send_timeout` and
  `kern_signal_fd`, in the hosted runtime (`runtime/hosted/src`), the
  interpreter (`compiler/interp`) and the LLVM backend's declarations,
  and a change to how `kern_signal_wait` empties its pipe (§3, R9);
- the examples `examples/kvstore` and `examples/supervise`, fixtures
  under `tests/run` and `tests/concurrency`, the generated tables
  (`docs/runtime.md`, `docs/reports/freestanding-availability.md`), and
  the report `docs/reports/std-streams.md`.

**Language impact:** none: library additions and two runtime entries.
**Follows:** `specs/runtime/SPEC.md` (its requirements are cited as
"runtime R*x*"), `specs/std-timeouts/SPEC.md` (the timed-out sentinel).

**Glossary.**
- R*n*: requirement *n* of §3. *Engine*: the interpreter or the native
  build; *configuration*: an engine at an opt level (the interpreter,
  native debug, native release).
- *Line terminator*: the byte `\n` (10), or the two bytes `\r\n` (13,
  10). A *whole line* is a run of bytes followed by a terminator; its
  *content* is the run without the terminator.
- *d*: a `time.Duration` argument; the *deadline* is the monotonic
  clock's reading at the call plus *d*.
- *Timed out*: an entry's result −2^32, the sentinel of
  `specs/std-timeouts`.
- *Arrived*: a signal has arrived when the runtime has recorded a
  delivery of it that no `wait`, `wait_timeout` or `try_next` has
  returned yet (runtime RI1).

## 0. Goal and non-goals

Five programs were written on the runtime on 2026-11-10 (`examples/kq`,
`kvstore`, `ksearch`, `supervise`, `httpd`). Each of the following had to
be written by hand in them, some more than once, or could not be written
at all:

1. splitting a stream of bytes into lines when the program both reads
   and writes the stream (`io.BufReader` owns its stream, so the program
   cannot write to it), or receives the bytes in pieces from a `Poller`;
2. removing the front of a `Vec` once it has been consumed;
3. writing to a socket without waiting for the peer, so that one thread
   serving many connections is not held by one that stops reading;
4. waiting for a signal and for sockets in one `Poller.wait`, instead of
   waking on a timer to look for a signal.

This spec adds those four, and `str.ends_with`, and makes two of the
examples use them.

Non-goals: timed writes on `process.Pipe`, `UdpSocket` and
`UnixDatagram`; a `LineBuf` over anything but bytes (no decoding); a
limit on what a `LineBuf` holds (the caller reads `pending`); closing
the runtime's signal pipe; `remove_prefix` on `VecIn` and `str.String`;
changes to `io.BufReader`.

## 1. Interfaces

```
// prelude
impl Vec[T] {
    func remove_prefix(edit self, n: usize)
}

// std/io
struct LineBuf nocopy { ... }
impl LineBuf {
    func new() -> LineBuf
    func feed(edit self, bytes: view[]u8)
    func next_line(edit self, line: edit[Vec[u8]]) -> bool
    func take_rest(edit self, line: edit[Vec[u8]]) -> bool
    func pending(view self) -> usize
    func read_line[R: Reader](edit self, r: edit[R], line: edit[Vec[u8]]) -> Result[bool, IoErr]
}

// std/str
func ends_with(s: view[]u8, suffix: view[]u8) -> bool

// std/net
impl TcpStream  { func write_timeout(edit self, data: view[]u8, d: time.Duration) -> Result[Option[usize], io.IoErr] }
impl UnixStream { func write_timeout(edit self, data: view[]u8, d: time.Duration) -> Result[Option[usize], io.IoErr] }
impl Source for signal.Signals { func descriptor(view self) -> i32 }

// std/signal
func signal_fd() -> i32      // the runtime entry, which the impl above calls
```

The runtime entries, in the hosted runtime's header and, with the same
meaning, as the interpreter's intrinsics `net_send_timeout(fd: i32,
data: view[]u8, ns: u64) -> i64` and `signal_fd() -> i32`:

```
// Sends up to n bytes of p on a socket, waiting at most ns nanoseconds
// for it to take any: the number taken (0 when n <= 0, with no system
// call), the negated host error number, or the timed-out sentinel when
// it took none in that time.
int64_t kern_net_send_timeout(int32_t fd, const char *p, int64_t n, uint64_t ns);
// Returns the read end of the runtime's signal pipe, or -1 when the
// process has never caught a signal.
int32_t kern_signal_fd(void);
```

## 2. Informative: how the pieces are used together

A server on one thread watches its listener, its connections and its
`Signals` with one `Poller`. A readable connection is read once and the
bytes fed to that connection's `LineBuf`; `next_line` is called until it
is false. Replies are appended to a `Vec[u8]` the connection owns and
sent with `write_timeout` of zero duration; what the socket took is
removed with `remove_prefix`, and when something is left the connection
is watched for writing too. A report for the `Signals` is followed by
`try_next` until it is `None`.

## 3. Requirements

**R1. `Vec.remove_prefix(n)`.** With *len* the vector's length before
the call: when `n > len` the call traps with kind `bounds` and the
vector is unchanged. Otherwise the elements at indexes 0 to `n - 1` are
destroyed, from index `n - 1` down to index 0, each exactly once; the
elements that were at indexes `n` to `len - 1` are then at indexes 0 to
`len - n - 1`, in the same order, none of them destroyed or copied by
`copy`; the length is `len - n` and the capacity is unchanged. `n` of 0
changes nothing. The call makes no allocation call.

**R2. `LineBuf` holds bytes.** A `LineBuf` holds a sequence of bytes,
empty when new. `feed(bytes)` appends `bytes` to it. `pending()` is the
number of bytes it holds. No other call adds bytes, and bytes leave it
only as R3, R4 and R5 say.

**R3. `next_line(line)`.** When the held bytes contain a `\n`: the call
returns true; the content of the first whole line is appended to `line`
(the bytes before the first `\n`, without the byte before it when that
byte is `\r`); those bytes and the terminator leave the `LineBuf`, and
the bytes after the terminator stay, in order. When the held bytes
contain no `\n`: the call returns false, and `line` and the `LineBuf`
are unchanged. A `\r` that is not followed directly by `\n` is content.

**R4. `take_rest(line)`.** When the `LineBuf` holds no bytes the call
returns false and changes nothing. Otherwise it returns true, appends
every held byte to `line`, exactly as held, whole lines and their
terminators included, and the `LineBuf` holds no bytes.

**R5. `read_line(r, line)`.** The call repeats: when `next_line(line)`
would return true, it does so and the call returns `Ok(true)`;
otherwise it calls `r.read` once with a buffer of 4,096 bytes; an `Err`
is returned as it is, with `line` unchanged and the `LineBuf` holding
what it held before that read; a read of 0 bytes ends the repetition;
any other read is fed. When the repetition ended on a read of 0 bytes,
the call returns `Ok(take_rest(line))`: a last line without a
terminator is a line, returned exactly as held, and `Ok(false)` means
the end with nothing left. Between calls the `LineBuf` holds whatever
was read past the line returned, so a program may read lines from `r`
through the `LineBuf` and write to `r` itself between them.

**R6. Cost.** The work a `LineBuf` does is linear in the bytes that
pass through it, not in the number of calls times the bytes it holds.
The fixture of §5 measures, in one run and with the same clock: (a)
one line of 65,536 bytes fed in a single `feed` and taken by `next_line`; (b)
the same line fed one byte at a time, with `next_line` called after
every byte; (c) 100,000 lines of 10 bytes fed in a single `feed` and
taken one by one. It requires (b) to take less than 400 times (a) plus
one second, and (c) less than 400 times (a) plus one second: an
implementation that rescans the held bytes on every call, or moves them
on every line, exceeds both by orders of magnitude (65,536 bytes
rescanned 65,536 times, or a megabyte moved a hundred thousand times).

**R7. `ends_with(s, suffix)`** is true exactly when `len(suffix) <=
len(s)` and the last `len(suffix)` bytes of `s` equal `suffix`; so it is
true for an empty `suffix`.

**R8. `write_timeout(data, d)`** on a `TcpStream` or a `UnixStream`.
For empty `data` the result is `Ok(Some(0))` and no system call is
made. Otherwise the call offers `data` to the socket once, and, when
the socket can take none of it, waits until the socket is writable or
the deadline passes, and offers it again:
- `Ok(Some(n))`, with `1 <= n <= len(data)`, when the socket took the
  first `n` bytes of `data`; the call does not go on to send the rest;
- `Ok(None)` when the deadline passed with nothing taken; nothing of
  `data` was sent;
- `Err(e)` for the failures `write` reports, with the same kinds (a
  closed peer is `BrokenPipe` or `ConnectionReset`, never a signal).
A `d` of zero offers once and never waits. A wait that ends because
the deadline passed is followed by no further offer: the result is
`Ok(None)`. Only a wait that reported the socket writable is followed
by another offer, and when that offer too takes nothing the call waits
again for the time that is left. A wait that a signal interrupts
continues with the time that is left. The call never blocks
past the deadline by more than the host's scheduling of the thread.

**R9. A `Signals` in a `Poller`.** `net.Source` is implemented for
`signal.Signals`: its `descriptor()` is a descriptor that is readable,
as `Poller` means it, whenever a signal the `Signals` has caught has
arrived, so `Poller.watch(sigs, token, true, false)`
and `Poller.unwatch(sigs)` are accepted. While the watch stands:
- (a) when a signal the `Signals` has caught arrives, a `Poller.wait`
  that is blocked or begins afterwards reports the token, `readable`,
  within the time the host takes to deliver the signal;
- (b) reports do not repeat without end: after `try_next` (or
  `wait_timeout`) has returned `None`, at most one further report
  comes before another caught signal arrives (none, when the report
  that led to the `try_next` came after the signal's handler had
  returned, which is the case in the fixture);
- (c) a report may come when nothing has arrived (after a signal was
  taken by a call that returned it): the program calls `try_next` until
  it is `None`.
A `Signals` exists only when the descriptor does: `signal.catch`
traps `explicit` when the runtime cannot make its pipe, as it did
before this spec, so `descriptor()` never fails.
`Poller.watch` of a `Signals` with `writable` set is an error of kind
`InvalidInput` and leaves any earlier watch of it as it was. The
descriptor is close-on-exec, is not inherited by a child, and stays the
same number for the life of the process. Destroying the `Signals` does
not end a watch: a `Poller` that still watches it reports nothing until
another `Signals` catches a signal that arrives, and then reports under
the token it was given. `wait`, `wait_timeout` and `try_next` return
what they returned before this spec in every case.

**R10. The same in every configuration.** The fixtures of §5 give the
same standard output in the interpreter, native debug and native
release, on macOS and on Linux.

**R11. The examples.** `examples/kvstore` reads its connections'
lines with a `LineBuf` each, sends replies with `write_timeout` of zero
duration, keeps what a socket did not take and watches that socket for
writing until it has taken it, stops reading a connection that is owed
more than 1 MiB of replies until it has taken them, and waits for
`Interrupt` and `Terminate` in its `Poller`, with no timed wake.
`examples/supervise` splits its children's output with a `LineBuf`.
Each still prints the output its `.out` file holds, changed only where
the demonstration itself changes (kvstore's is stopped by a signal).

## 4. Errors and edges

- `remove_prefix` on an empty vector with `n` of 0 is a no-op; a vector
  of zero-size elements behaves as any other.
- `feed` of an empty view changes nothing. `feed` traps `oom` as
  `Vec.extend` does when memory cannot be had; there is no fallible
  form.
- `next_line` and `take_rest` append to `line`; they never clear it.
  When `line` cannot grow they trap `oom`, as `Vec.extend` does, and so
  does `read_line`, which reports only `r`'s errors as `Err`.
- A terminator split between two `feed` calls (`\r` at the end of one,
  `\n` at the start of the next) is one terminator.
- `write_timeout` on a socket whose peer has shut down its reading
  side fails as `write` does.
- `Signals.descriptor()` is the same descriptor for every `Signals` of
  the process.

## 5. Tests

- `tests/run/vec_remove_prefix.kern`: R1 over `u64` and over a type
  whose destructor prints (order, exactly once, the rest intact, the
  capacity), `n` of 0 and of the whole length;
  `tests/trap/vec_remove_prefix_bounds.kern`: `n > len` traps `bounds`.
- `tests/run/io_linebuf.kern`: R2 to R5 and the edges of §4 that
  concern `LineBuf`, with a `Reader` that returns its bytes in pieces
  of chosen sizes and one that fails on its third read; R6's two loads,
  timed with `now_ns` and reported as "within the limit".
- `tests/run/std_str.kern` gains R7's cases.
- `tests/concurrency/net_write_timeout.kern`: R8 over TCP and a Unix
  pair: a peer that does not read is written to until `Ok(None)`, then
  reads, and the writer goes on; the bytes received are the bytes the
  calls said were taken, in order; zero duration; empty data; a closed
  peer.
- `tests/concurrency/signal_poller.kern`: R9 (a) to (c) with a socket
  watched beside the `Signals`, the `InvalidInput` case, and `unwatch`;
  `tests/concurrency/signal_poller_wakes.kern`: a signal that arrives
  while the wait blocks. The second is not run under ThreadSanitizer,
  which on macOS runs a signal's handler only when the thread that
  received it enters a call the sanitizer intercepts, and a wait in the
  readiness queue is not one.
- The examples' own fixtures: R11.
- `go test ./...`, the sanitized corpus and `scripts/linux-check.sh`
  pass.
