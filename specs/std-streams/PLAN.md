# Plan: line buffers, prefix removal, timed writes, signals in a Poller

**Review status.** plancritic (gpt-6.1-sol, with the spec as context)
round 1: EXECUTABLE_WITH_CLARIFICATIONS, no critical finding; its five
warnings are answered in place: the compaction resets `start` (phase
2), the interpreter's timed write has one path for a zero duration and
one for the rest, with empty data and interruptions stated (phase 3),
and the cases of the two socket fixtures are listed (phases 3 and 4).

Implements `specs/std-streams/SPEC.md` v0.3 in five phases. Each phase
ends with its fixtures passing in the three configurations on macOS;
the generated tables and the Linux gate come in phase 5, because phases
3 and 4 change what the library reaches and the tables are checked by
tests that fail until they are regenerated.

Conventions that hold in every phase: `std/*.kern` and
`compiler/resolve/prelude/*.kern` are byte-identical (edit `std/`, copy
to the prelude); a fixture's expected output is written from the spec
before the fixture is run; `gofmt -l` and `golangci-lint run ./...`
are clean before a phase ends.

## Phase 1: `Vec.remove_prefix` and `str.ends_with` (R1, R7)

**Files.**
- `std/core.kern` (and the prelude copy): `Vec.remove_prefix(edit self,
  n: usize)` beside `clear`, with a contract block.
  - `n > self.size` is `trap_kind(bounds)` before anything is touched.
  - The elements `n - 1` down to 0 are taken with `ptr_take` and
    dropped, one by one, as `clear` does for its range.
  - The `size - n` elements that remain are moved to the front with one
    `ptr_copy` (overlapping ranges are permitted, Rule 6.6); `size`
    becomes `size - n`. No allocation call; `cap` untouched.
- `std/str.kern` (and the prelude copy): `ends_with` beside
  `starts_with`.
- `tests/run/vec_remove_prefix.kern` + `.out`,
  `tests/trap/vec_remove_prefix_bounds.kern`, and cases added to
  `tests/run/std_str.kern` + `.out`.

**Acceptance.** The three fixtures pass under `go test ./tests -run
'TestFixtures$/(run|trap)/(vec_remove_prefix|std_str)'`, and
`vec_remove_prefix` under the sanitized run.

**Risk.** A trap inside a destructor during the removal leaves the
vector partly consumed; that is the same as `clear`, and a trap ends
the program (Rule 5.7.4).

## Phase 2: `io.LineBuf` (R2 to R6)

**Files.**
- `std/io.kern` (and the prelude copy), after `BufWriter`:
  `struct LineBuf nocopy { buf: Vec[u8]; start: usize; scanned: usize }`.
  - `buf[start:]` are the held bytes; `scanned` is an index at or after
    `start` up to which no `\n` is known to lie.
  - `feed` extends `buf`. Before extending, when `start` is at least
    4,096 and at least half of `buf.len()`, it calls
    `buf.remove_prefix(start)`, lowers `scanned` by `start` and sets
    `start` to 0: a byte is moved at most once per halving, which is
    R6's bound. The fixture has the case: more than 4,096 bytes and more
    than half the buffer consumed, an unfinished line left, then more
    fed and taken.
  - `next_line` scans from `scanned` for `\n`; on none, `scanned`
    becomes `buf.len()` and the result is false; on one at `i`, it
    appends `buf[start:i]` less a final `\r`, sets `start` and
    `scanned` to `i + 1`, and, when `start == buf.len()`, clears `buf`
    and zeroes both.
  - `take_rest` appends `buf[start:]`, clears `buf`, zeroes both.
  - `pending` is `buf.len() - start`.
  - `read_line[R: Reader]` is R5's loop over a `[4096]u8` on the stack.
- `tests/run/io_linebuf.kern` + `.out`: R2 to R5, §4's edges, two
  `Reader`s declared in the fixture (pieces of chosen sizes; an error on
  the third read), and R6's three measurements printed as one line,
  "within the limit" or the three numbers.

**Acceptance.** The fixture passes in the three configurations and
under the sanitized run; R6's line reads "within the limit" in each.

**Risk.** R6's measurement is of time. The limit is relative to a
measurement of the same run and loose by two orders of magnitude, and
the quadratic implementations it excludes miss it by three or more.

## Phase 3: timed writes (R8)

**Files.**
- `runtime/hosted/src/rt_net.c`, `kern_rt.h`: `kern_net_send_timeout`
  after `kern_net_send`, the loop of `kern_net_recv` with `POLLOUT`:
  `n <= 0` is 0; `send` with `KERN_SEND_FLAGS`; `EINTR` retried; a
  would-block error waits with `kern_net_wait(fd, POLLOUT, 1, start,
  ns)`, whose 0 is `KERN_TIMED_OUT`. `kern_net_wait` already returns 0
  at once when the time is spent, so a zero `ns` offers once.
- `compiler/backend/llvm/calls.go`: `net_send_timeout` calls
  `kern_net_send_timeout` as `i64 (i32, ptr, i64, i64)`.
- `compiler/interp/net.go`: `net_send_timeout`, through the
  connection's `SyscallConn().Write`, whose function makes one
  `syscall.Write` and retries it while it is interrupted (`EINTR`).
  - Empty data is 0 before the descriptor is looked up.
  - A zero duration: no deadline is set and the function returns true
    whatever the write said, so Go never waits: one offer.
  - Any other duration: the write deadline is set to now plus it
    (clamped as the timed reads clamp), and the function returns false
    on would-block, so Go waits for writability and calls it again, and
    fails the call once the deadline passes without calling it again.
    That is R8's sequence: no offer follows an expired wait.
  - Bytes taken are the result; a deadline error is `timedOut`; any
    other error is its negated number; the deadline is cleared by a
    `defer`.
- `std/net.kern` (and the prelude copy): the intrinsic's declaration
  and `write_timeout` on `TcpStream` and `UnixStream`.
- `tests/concurrency/net_write_timeout.kern` + `.out` (`tsan: clean`),
  each case over TCP and over a Unix pair: empty data is `Some(0)`; a
  small write is taken whole; a peer that does not read is written to
  with a zero duration until `None`, then with 50 ms, which is `None`
  after at least 50 ms; the peer then reads everything, and the count
  it read is the sum of the `Some(n)` the writer was given, the bytes
  in order; after that a write is taken again; a write after the peer
  has closed is an error of kind `BrokenPipe` or `ConnectionReset`.

**Acceptance.** The fixture passes in the three configurations, under
ThreadSanitizer and under the sanitized run.

**Risk.** How many bytes a socket's buffer takes differs by host, so
the fixture prints no counts: it checks that the writer reached
`Ok(None)`, and that the bytes the reader got are exactly the bytes the
calls reported, in order.

## Phase 4: a `Signals` in a `Poller` (R9)

**Files.**
- `runtime/hosted/src/rt_signal.c`, `kern_rt.h`: `kern_signal_fd`
  returns `kern_sig_pipe[0]`. `kern_signal_wait`, when no wanted signal
  is recorded, empties the pipe and looks at the bits once more before
  it gives up or waits: the pipe is then readable only for a record
  made after it was emptied (R9 b), and a record made between the first
  look and the emptying is found by the second look (no lost wake).
- `runtime/hosted/src/rt_net.c`: `kern_net_poller_set` returns
  `-EINVAL` for write interest in the descriptor `kern_signal_fd()`
  returns, before it changes anything.
- `compiler/backend/llvm/calls.go`: `signal_fd` calls `kern_signal_fd`
  as `i32 ()`.
- `compiler/interp/signal.go`, `net.go`: a pipe of the process's own
  (non-blocking, close-on-exec), made at the first `signal_open`; every
  place that records an arrival writes one byte to it; `signalWait`
  empties it as the runtime does; `signal_fd` returns a descriptor of
  the interpreter's socket table that stands for the pipe's read end,
  the same one each time for a program, which the poller watches by its
  system number, refuses for writing, and which is not closed when the
  program ends.
- `std/signal.kern`: the intrinsic's declaration and
  `Signals.descriptor`. `std/net.kern`: `import "std/signal"` and
  `impl Source for signal.Signals` (and the prelude copies).
- `tests/concurrency/signal_poller.kern` + `.out` (`tsan: clean`):
  with a `UnixStream` pair watched beside the `Signals`: nothing is
  reported before a signal; a signal raised before `wait` is reported
  under the token; one raised by another thread while `wait` blocks
  wakes it; after `try_next` has returned `None`, a zero-duration wait
  reports nothing (R9 b); a byte on the pair is reported under its own
  token while no signal waits; a watch with `writable` set is
  `InvalidInput` and the earlier watch still reports; after `unwatch` a
  signal is not reported, and `try_next` still returns it.

**Acceptance.** The fixtures pass in the three configurations and under
the sanitized run, `signal_poller` under ThreadSanitizer too (the case
of a signal that arrives while the wait blocks is a fixture of its own,
`signal_poller_wakes`, which the sanitizer cannot run on macOS: it
defers a handler until the thread enters a call it intercepts, and the
readiness queue's wait is not one); `signal_basics`,
`signal_children` and `signal_interrupts` pass unchanged (R9's last
sentence).

**Risks.**
- The change to `kern_signal_wait` is in the path of every wait: the
  three existing signal fixtures and `TestSignal*` in `tests/` are the
  check that nothing a wait returns has changed.
- `std/net` importing `std/signal` makes every program that imports
  `std/net` compile `std/signal`; the hosted-only inventory grows by
  the entries `Signals` reaches, and is regenerated in phase 5.

## Phase 5: the examples, the generated tables, the report, the gates (R10, R11)

**Files.**
- `examples/kvstore/kvstore.kern` + `.out`: a `LineBuf` per connection;
  replies sent with `write_timeout` of zero duration and
  `remove_prefix`; a connection with replies left is watched for
  writing as well; one owed more than 1 MiB is not read until it has
  taken them; `serve` takes the `Signals` and watches it, and
  `Poller.wait` has no time limit. The demonstration stops its server
  with `signal.raise(Terminate)`, after the conversation, which the
  server's catch precedes because the server answers only after it.
- `examples/supervise/supervise.kern`: `absorb` becomes a `LineBuf`.
- Generated: `go test ./compiler/analysis -run TestRuntimeEntryTable
  -update-runtime-doc`, `-run TestHostedOnlyInventory
  -update-hosted-inventory`.
- `docs/runtime.md` (the sections on sockets and on signals),
  `CHANGELOG.md` under `[Unreleased]`, `docs/reports/std-streams.md`.

**Acceptance (the final gates).**
1. `gofmt -l compiler cmd tests runtime` prints nothing;
   `golangci-lint run ./...` reports no issue.
2. `go test ./... -timeout 90m` passes.
3. `KERN_SANITIZE=1 go test ./tests -run
   '^(TestFixturesSanitized|TestSanitizerDetects)$' -count=1` passes.
4. `KERN_SANITIZE=1 scripts/linux-check.sh --run "./compiler/...
   ./runtime/... ./cmd/... -timeout 60m" --run "./tests -timeout
   120m"` passes.
5. `prism review` of the change finds nothing that is left unanswered.

**Risk.** kvstore's demonstration raises a real signal in the test
process when it runs in the interpreter. The harness runs fixtures one
at a time and `signal_basics` already does this; the interpreter
restores the process's signals when a program ends.
