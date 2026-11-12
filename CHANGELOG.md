# Changelog

All notable changes to kern are recorded here. The layout follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) in its 0.x form:
see "Versioning and stability" in the README. The procedure that turns a
section of this file into a GitHub release is `docs/releasing.md`.

## [Unreleased]

Five more examples, written to find flaws, and what they found.

### Language

- **A view of what a lock guards could outlive the lock.** `with g :=
  m.lock() { return g }` was accepted (and the same through a field of
  `g` or a `match` arm's binding inside the `with`), while `return
  m.lock()` was refused: the caller then read what another thread may
  write. It is `ACC_RELEASE_ESCAPE` now. A program that did this no
  longer compiles; copy the value out inside the `with`.
- **`match v? { ... }` on a variable never destroyed what `?`
  unwrapped.** The variable counted as moved while the arms were given
  views, so the payloads' destructors did not run (descriptors held in
  them leaked). What `?` unwraps is now an owned subject whatever it
  was applied to, as it was for a call: the arms own the payloads.
- A `match` whose subject is a part of what an accessor returned
  (`v.get(i).field`) lost the part: both engines failed on a program
  the checker accepted. An arm of such a match with no access binding
  now leaves the container free, as Rule 5.3.1 says (an edit of `self`
  in it was an internal error), and through an edit accessor
  (`v.at(i).field`) the arm's bindings are edits (they were read-only).
- What an accessor returns is an assignment target, as Rule 5.3.3 says:
  `v.at(i) = x` and `v.at(i).f = x` were each refused as a conflict of
  the projection with itself. The value is evaluated before the
  accessor is called.
- An accessor may return a view reached through a `match` arm's binding
  inside a `with` (each alone was accepted, the two nested refused).
- An interface's function without a receiver is called through a type
  parameter, `T.f(...)` (it was refused with "`()` has no method");
  called on a value it is `TYPE_MISMATCH` (it was an internal error).
  `spec/types.md` says so.
- A type's static function is a value, `g := A.make` and
  `W[u32].wrap` (it passed the check and was an internal error); with a
  type argument missing it is `TYPE_INFER_FAILED`.
- A function the runtime provides (`print_u64`) is not a function value
  (`TYPE_MISMATCH`; it was accepted and failed in both engines), and an
  array literal cannot hold string literals (`ACC_ESCAPE_STORE`; the
  same).
- A conversion reads through an accessor's `Copy` result
  (`u64(v.get(i))`), and an array an accessor returned coerces to a
  sequence argument as an array place does (Rule 5.2.4).
- Five examples, longer than the earlier ones (1,300 to 2,100 lines):
  `kdb` (a B+tree of 4,096-byte pages in one file: `load_le` and
  `store_le` page layouts, a bounded cache grown with the `try_` forms,
  two header slots, range locks between processes, a header read
  through a writable `mem.Mapping`), `kvm` (a small scripting language:
  a Pratt parser into an `ArenaIn`, a bytecode compiler with closures,
  a stack machine with a mark-and-sweep heap in a `Pool`, every guest
  error reported without a trap), `kpass` (a pre-forking server:
  connections passed as `Descriptor`s over `UnixStream`s, peer
  credentials, one `Poller` over listeners, workers, a `UnixDatagram`
  status socket and signals, `time.Deadline`s for idle connections and
  shutdown), `ksim` (a discrete-event simulator: interfaces and generic
  containers of its own, `rand` streams, `time.Duration` with checked
  arithmetic, entities in a `PoolIn` and an `ArenaIn` over a bounded
  `Allocator`) and `ksum` (SHA-256, SHA-512, SHA-1, MD5, BLAKE2s and
  CRC-32C behind one interface, HMAC, PBKDF2 and HKDF generic over it,
  compression functions under `budget { allocations 0; bounds_checks 0
  }`, a reader, hashing workers and an ordered writer joined by
  `sync.Channel`s).

- Two files of one module with `move x` or `drop(x)` of the same name at
  the same byte offsets (files begun from one template) passed the check
  and then failed with an internal error in most runs: the checker and
  the lowering found the variable by position and name across the whole
  program and took the other file's.

### Library

- `Vec.clone` (and so `String.clone`) writes its copies straight into
  the new storage: a megabyte of bytes took 50 times the instructions
  through `push`.
- A vector's first growth gives it 16 bytes' worth of one- to three-byte
  elements, not four of them: a `String` built by pushes reallocated
  three times inside the allocator's smallest block (`json.parse` of a
  2.6 MB document, 24% fewer instructions). `capacity()` of a small byte
  vector reads 16 where it read 4 or 8.
- `map.Map` is `map.MapIn` over the system allocator, with one probe,
  one growth rule and one rebuild in the file: 2M inserts and 8M finds
  of `u64` keys take 525 M instructions and 138 MB where they took
  1,560 M and 278 MB. Slot order, iteration order and the order values
  and keys are destroyed in are what they were (a new fixture pins
  them). `Map`'s fields and its helpers `some_offset`, `place_opt` and
  `insert_opt` are gone, and `Map.rebuild` called directly with too few
  slots builds the smallest table that fits in one step. `Map.try_rebuild`
  now does the same: asked for fewer slots than the map has entries it
  never returned.
- `str.equal`, `starts_with`, `ends_with` and `find` compare eight bytes
  at a step, and `str.find_byte` (new) finds a byte the same way; the
  line readers (`BufReader.read_line`, `LineBuf`) find their newline with
  it, so `std/io` imports `std/str`. On long inputs a native build takes
  0.9 to 1.5 instructions a byte where it took 6 to 9 (`find` with a
  common first byte 4.9 for 9.8), and none of these reads is
  bounds-checked at run time. Short inputs pay for it in places: `find`
  in a text under about 9 bytes costs 30 to 45 instructions more a call,
  `find_byte` under 8 bytes 2 to 11 more, `equal` of 1 to 3 equal bytes
  1 to 3 more (from 4 bytes it is faster).
- `needs_drop[T]()` says whether destroying a `T` does anything; `Vec`,
  `VecIn`, `Arena` and `Pool` ask it before a loop that takes and drops
  each element, which for plain elements only counted down (a native
  build deleted it; the interpreter ran it an element at a time).
- `io.read_file` asks a regular file's size and reads into a vector of
  that size, where it read 4,096 bytes at a time through a buffer (a
  256 MiB file: 570M instructions to 173M); pipes and terminals are read
  16 KiB at a time.
- `UnixStream.send_with` and `docs/runtime.md` say that the sender
  keeps its descriptor until the receiver has taken the one sent: on
  macOS a socket closed by its sender while in flight can arrive
  already at its end, with what its peer wrote lost.

### Runtimes and targets

- Every allocation wrote one flag shared by all threads (the test hook
  for a failed allocation, exchanged where it only needed reading): four
  threads allocating took nine times as long as one. It is read first
  now, and four threads take about a fifth of the cycles they did.
- The system allocator uses `malloc` for an alignment of 16 or less,
  where it always called `aligned_alloc`: an allocation and its free
  take 18% fewer instructions.
- `swap_at` is three constant-size copies in the program's own code,
  where it was a call of the runtime that copied through a buffer: a swap
  of two `u64` goes from 123 instructions to about 10. The runtimes'
  `kern_swap` entry is gone with its last caller.

### Tooling

- `kern run`, `kern test` and every command but `kern lsp` let Go's heap
  reach 64 MiB before a collection starts (nothing changes above that,
  and `GOGC` or `GOMEMLIMIT` in the environment is obeyed as before).
  With a few megabytes live the collector was 20 to 45% of what the
  interpreter executed: example demonstrations run 20 to 40% fewer
  instructions in about half the processor time, for 50 to 60 MB more
  memory.
- The interpreter makes the host's network poller before a program
  runs. On macOS it is a descriptor made at first use and kept, so a
  program that counted its open descriptors before its first socket and
  after its last could find one more than it opened; it had existed at
  start only when a collection during start-up happened to make it
  (`TestConnectsThatFailLeaveNoDescriptor` failed now and then for that
  reason, and every time under the heap floor).
- A value of more than 128 bytes crosses a call in memory: a by-value
  parameter is a pointer the callee copies from, and a function's one
  result is written through a pointer its caller passes. As a first-class
  LLVM value a struct of a thousand fields was a thousand registers at
  every call and return: kzip's release build takes 0.60 s where it took
  2.06 s, its debug build 0.40 s for 1.82 s, and its code is 40% smaller.
  Functions under a C name keep C's convention. Ten benchmark workloads
  retire the same instructions within 0.5%.
- The interpreter's raw memory is reused: a freed block goes back to a
  list for its size once 16 MiB has been freed after it, where memory
  was never given back (a loop that allocates and frees 16 KB held
  1.5 GB; it holds 111 MB, most of it the collector's floor). The chunk
  an address lies in is found by a search without a lock, and the table
  of handles is bounded by the objects alive. A use after free of a
  block over 128 KiB traps once the block has left quarantine.
- The interpreter answers `size_of`, `align_of` and `needs_drop` when it
  compiles a call of them. With the destroy loops gone the examples'
  demonstrations run 5 to 31% fewer instructions (ksort and kzip 31%).
- The interpreter reads `load_le` and `load_be` from raw memory as one
  span, where it made a place and decoded a value for each byte (about
  4,700 host instructions for a `u64`). With the word-wise `std/str`
  kcache's demonstration runs 8% fewer instructions than before either
  change and kvstore's 2% more.
- `go test ./tests` builds the fixtures' native programs ahead of the
  fixture loop, 16 at a time (`KERN_TEST_JOBS` sets the number), and
  the self-hosted parser's diagnostics test runs its inputs through a
  pool: the package takes 245 s where it took 350 to 385 s on the same
  loaded host, with the same subtests and results.
- `kern show-layout` listed `u64`, `usize`, `u8`, `i64` and `u32` as
  types of size 0 in any program that imports `std/map` (what its
  `impl Hashable for u64` and the others declare); they are left out.
- The language server converts positions through a table of line starts
  made once for a document's text: it decoded the whole file at every
  conversion, four times a symbol in an outline (`kvm.kern`'s outline,
  2,700 M instructions and 138 MB to about 360 M and 32 MB; a file of
  16,000 lines, 4.5 GB allocated to 139 MB). The answers are the same.
- The interpreter compiles an indexed load or store, `len`, and a
  union's tag (a tag of a container's element was read by decoding the
  whole union): with the above, ksum's demonstration runs 57% fewer
  instructions, klisp's 46%.
- The IR validator keeps a function's locals in a slice, not a map
  copied at every block: it was about 60% of a release build's front end
  (kvm, 1.79 G instructions to 0.83 G with the heap floor; the IR is
  byte-identical).
- `bench/REPORT.md`'s text bytes count a kern program's own code: the
  harness read only `__text`, which in a kern binary is the C runtime.
- The interpreter read every element of an array held in a `Box` or a
  container each time it checked an index into it through a sequence
  parameter (55 µs an access for 4,096 bytes; a length now comes from
  the type).
- The README said `kern run` runs a program natively; it runs the
  interpreter.
- `spec/types.md` said the library modules export every declaration;
  it now also says what the compiler does for fields: every field of a
  `nocopy` library struct is private to its module.

### Examples

- `examples/kawk` is the AWK language in 6,200 lines of kern: patterns
  and ranges, every statement and operator, user functions, associative
  arrays, fields and records under `FS` and `RS`, `getline`, `printf`,
  pipes and `system`, with POSIX extended regular expressions matched
  leftmost-longest, `%e %f %g` from the exact expansion of a double, and
  `sqrt`, `exp`, `log`, `sin`, `cos` and `atan2` of its own (the library
  has none of these). It was checked against the system's awk (the
  one-true-awk, version 20200816) on 548 programs, byte for byte in
  output and exit status, natively and in the interpreter; on a file of
  200,000 lines it retires 0.08 to 0.49 of that awk's instructions.

## [v0.2.3] - 2026-11-11

One defect of v0.2.2: the lost write that release fixed for a `match`
arm's binding was still there for a `for` loop's variable.

- A write through a `for` loop's variable of a `Copy` element was lost:
  the body changed a copy and the sequence stayed as it was.

It is under "Language", with the three ways it can alter a program
written for v0.2.2. Only a program that writes through such a variable
is affected: over the 2,127 kern files of this repository's tests,
examples, benchmarks and library, three were. Rule 5.3.1 of the
reference and the cursor-protocol paragraph of `spec/types.md` are
reworded to say what such a variable is (`docs/reports/for-bindings.md`).
`spec/rules.lock` is unchanged, and the machine API answers the schema
versions it answered in v0.2.2 (0.2 to 0.5). CI's toolchain install
steps now stop after fifteen minutes.

### Language

- Fixed: a write through a `for` loop's variable of a `Copy` element was
  lost. `for b in data { b = table[usize(b)] }` changed a copy of each
  element and left `data` as it was, in both engines; the reference said
  so, and the same text in a generic function did write to the
  sequence. The variable is now an edit of the element when the body
  uses it as one, by the rule v0.2.2 gave a `match` arm's binding, and
  through a nested `match`, `with` or `for` on it as well; over a map
  (`for k, v in m { v = v + 1u64 }`) the loop then uses the editing
  getter. A variable the body only reads is still a copy, and a
  function with no such write is compiled to the code it was compiled
  to. Three kinds of program change, and no other:
  - in a loop over an edit (an `edit` sequence, a mutable array, a
    mutable map's value), the write now reaches the element;
  - in a loop over a view, and for a map's key, the write no longer
    compiles (`ASSIGN_IMMUTABLE`, or `ACC_PERMISSION_ESCALATION` for an
    `edit` argument, an `edit self` method, `take` and `replace`): copy
    the variable into a local to keep the old meaning;
  - a map loop that writes its value and has a loop over the same map
    nested in it no longer compiles (`ACC_CONFLICT`), as a loop that
    edits values that are not `Copy` never did.
  A range loop's variable is unchanged.

## [v0.2.2] - 2026-11-11

Two defects of v0.2.1 that five more programs written on it found, with
the programs themselves.

- A write through a `match` arm's binding of a `Copy` payload field was
  lost: the arm changed a copy and the matched value stayed as it was.
- A generic function used as a value with its type arguments failed in
  the compiler with an internal error.

Both are under "Language". The first can alter a program written for
v0.2.1 in three ways, listed in its entry: the write now reaches the
matched value; with a subject that is only a view it no longer compiles;
and an arm that writes its binding and also names the matched value no
longer compiles. Only a program that writes through such a binding is
affected: over the 2,113 kern files of this repository's tests,
examples, benchmarks and library, one fixture was. Rule 5.3.1 of the
reference is reworded to say what a binding of a `Copy` field is.
`spec/rules.lock` is unchanged, and the machine API answers the schema
versions it answered in v0.2.1 (0.2 to 0.5); an `ACC_CONFLICT` that a
write through an arm binding causes now fills the `related` list the
schema already has.

### Language

- Fixed: a write through a `match` arm's binding of a `Copy` payload
  field was lost. `match o { Some(n) => { n = n + 1u64 } ... }` changed a
  copy of the field and left `o` as it was, in both engines, although
  Rule 5.3.1 says an arm's bindings are edits of the matched place when
  the subject is one, and although the same text in a generic function
  did write to the subject. Such a binding is now an edit of the field
  when the arm uses it as one: assigns to it or to a part of it, passes
  it to an `edit` parameter or an `edit self` method, `take`s or
  `replace`s it, returns it as an edit, or does one of these through a
  nested `match` or a `with` on it. A binding the arm only reads is
  still a copy made on entering the arm, and still leaves the matched
  place free, which the reference now says. Three kinds of program
  change, and no other:
  - with a subject that is an edit, the write now reaches the subject;
  - with a subject that is a view, the write no longer compiles
    (`ASSIGN_IMMUTABLE`, or `ACC_PERMISSION_ESCALATION` for an `edit`
    argument, an `edit self` method, `take` and `replace`): copy the
    binding into a local to keep the old meaning;
  - an arm that writes its binding and also reaches the matched place by
    name no longer compiles (`ACC_CONFLICT`, which points at the write).
- A `match` keeps its subject frozen only in the arms that hold an
  access to it. An arm that binds nothing, or only `Copy` values it
  reads, may use and reassign the subject, as Rule 5.3.1 has it ("for
  the arm"): `match o { Some(n) => { n = n + 1u64 } None => { o = Some(0u64) } }`
  compiles. Until now one such binding in any arm froze the subject in
  every arm. No program that compiled is affected.
- Fixed: a generic function used as a value with its type arguments,
  `thread.spawn(work[u8], ...)` or `f := work[T]` inside a generic
  function, was accepted by the checker and then failed in the compiler
  with an internal error. It is now a reference to that instance, as
  the checker's own message for a bare `work` asks the program to write.

### Examples

- Five more applications in `examples/`, each a working tool that with
  no arguments runs a demonstration of itself, on parts of the language
  and library the first five did not use: `klisp` (a Lisp interpreter:
  a module of its own with tests, a union of cells in a `Pool`, a
  mark-and-sweep collector, an interactive prompt), `ksort` (an external
  merge sort under a memory limit: an `Allocator` of the program's own
  that counts, `VecIn`, generic functions that take their order as a
  closure, buffered files), `kzip` (gzip in both directions, compatible
  with the system's: bit streams, Huffman codes, tables built once,
  `budget { allocations 0 }` on the inner loops), `kcache` (a cache with
  a time to live and a bound on its size: `sync.RwLock` shards, an
  interface for the clock that the store is generic over, a sweeping
  thread on a `Condvar` with `Once` and `WaitGroup`, a thread-local
  count, UDP and signals in one `Poller`) and `kray` (a ray tracer:
  `f64` vectors on the SIMD intrinsics, shapes behind an interface with
  no dispatch at run time, rows shared out by an atomic counter across
  the threads of a scope, a PPM file).

## [v0.2.1] - 2026-11-11

Two defects of v0.2.0 that safe code reached, both found by writing
programs on it, with the library those programs asked for and the
programs themselves.

- A scoped thread's handle (`Join`) that was used or destroyed after
  its block read freed memory in a native program (under "Runtimes and
  targets").
- A lock taken inside a statement, `bump(m.lock())`, was never
  released (under "Language").

Three changes can alter a program written for v0.2.0, each listed in
its place below: destroying a `Join` no longer waits for its thread;
the results of handles left to the end of a scope's block are destroyed
in the order of the spawns, where it was the reverse order of the
handles; and `std/net` imports `std/signal`. `spec/rules.lock` is
unchanged, and the machine API answers the schema versions it answered
in v0.2.0 (0.2 to 0.5).

### Language

- Fixed: an access that came with a release function and was passed as
  an argument ended without the release function being called, in both
  engines. `bump(m.lock())` left the `Mutex` locked for good, and the
  next lock on that thread trapped; a `with` block released as it
  should. The release function now runs when the call that took the
  access returns, or, when that call returns an access derived from it,
  when that access ends (Rule 5.3.5, whose wording now says so).
- The reference no longer speaks of payload fields a pattern leaves
  unbound: a pattern names every field of its variant.

### Standard library

- `io.LineBuf` splits bytes into lines without owning where they come
  from: the program feeds it what it has read (from a socket it also
  writes to, or in pieces as a `Poller` reports them) and takes whole
  lines out with `next_line`; `read_line` does both over any `Reader`.
- `TcpStream.write_timeout` and `UnixStream.write_timeout` send what the
  socket will take within a duration; with no duration they never wait,
  which is how one thread writes to many connections.
- A `signal.Signals` can be watched by a `net.Poller`, so one wait
  serves sockets and signals. `std/net` imports `std/signal` for it.
- `Vec.remove_prefix(n)` drops the first `n` elements and keeps the
  rest in place and in order.
- `str.ends_with`.

### Runtimes and targets

- Fixed: in a native program, the handle of a scoped thread (`Join`)
  that was asked for its result, or destroyed, after the block of its
  scope had ended read memory the scope had freed. A handle now keeps
  its thread's record for as long as it lives, so `get` works inside the
  block and after it, as the reference says (Rule 5.5.3).
- Destroying a `Join` no longer waits for its thread. Inside the block
  it leaves the result to the scope, which destroys it when it joins the
  thread, in the order the threads were spawned; after the block the
  handle destroys the result itself. Workers spawned in a loop whose
  handles are dropped there no longer stop the loop, and the results of
  handles left to the end of a block are destroyed in the order of the
  spawns, where it was the reverse order of the handles.
- The runtime's interface version is 3 (`kern_rt_abi_3`): the contracts
  of `kern_join_get`, `kern_join_drop` and `kern_scope_drop` changed.
  Two entries were added, `kern_net_send_timeout` and `kern_signal_fd`,
  and `kern_net_poller_set` refuses write interest in the signal
  descriptor.

### Tooling

- The test suite gained fixtures for behaviour the reference states and
  nothing exercised (`docs/reports/reference-conformance.md`): release
  accessors at every kind of exit, destruction at every scope exit, the
  arithmetic traps beyond one sum and one division, the order of a
  pool's checks, slot retirement, a trap inside a destructor, and who
  destroys a value that threads share. A trap fixture may now state what
  the program wrote before it trapped, and the examples that run are
  part of the sanitized corpus.

### Examples

- Five applications written on the runtime, under `examples/`. Each is a
  tool to use (its usage is the comment at its head) and, run with no
  arguments, a demonstration of itself whose output the test suite
  compares in both engines:
  - `kq`: queries over a JSON document, in the manner of jq (paths,
    `[]`, `length`, `keys`, `type`, `select`, pipes), with results that
    are views of the document passed through closures.
  - `kvstore`: a key-value server that one thread runs for every
    connection on a `net.Poller`, with an append-only log synced once a
    round before the round's replies are sent, replayed at start, and
    rewritten by `COMPACT`. It writes without waiting for a client to
    read, and sleeps until a connection or a signal has something for
    it.
  - `ksearch`: a search for a pattern (a small regular-expression
    dialect) over directory trees: one thread walks, the others search
    mapped files, and the results are sorted by path.
  - `supervise`: starts the programs a JSON configuration names, passes
    on their output line by line, restarts them by policy with a growing
    delay, and on Interrupt or Terminate stops them, with Kill after a
    grace.
  - `httpd`: serves a directory over HTTP/1.1 from a pool of threads fed
    by a channel, with a time limit on a request's head, directory
    listings, and nothing served from outside its root.

## [v0.2.0] - 2026-11-10

The runtime: what runs under a kern program, on a host and on a board, and
the standard library that reaches it (files, sockets, signals, random
numbers, synchronization, memory). The specification is `specs/runtime`,
the reference `docs/runtime.md`, the record
`docs/reports/runtime-summary.md`.

Four changes can alter a program written for v0.1.0, each listed in its
place below: `io.FileKind` gained `Symlink`, so an exhaustive `match` on it
needs the arm; a thread that cannot be spawned is `Err(SpawnErr)` where it
was a trap; a new file's mode is 0666 less the umask; and assignment
destroys the previous value, so a destructor that did not run now runs.
`spec/rules.lock` is unchanged, and the machine API answers the schema
versions it answered in v0.1.0 (0.2 to 0.5).

### Language

- String literals take `\r` and `\xHH` (a byte by two hexadecimal
  digits).
- `unsafe assert Send` and `unsafe assert Share` may carry a `where`
  clause over the type's parameters; the type has the marker only for
  arguments that satisfy it.
- A variant name that several unions of a module declare is the expected
  type's when written without its union; it is ambiguous only where
  nothing says which is wanted.
- An owned `match` moves a payload out also when the payload's type has a
  destructor (only a union that declares a destructor itself is refused),
  and its subject may be what `take`, `replace` or `?` yields.
- `swap_at(xs, i, j)` exchanges two elements of a sequence of any type.
- Fixed: a `?` in an operand of a call, a struct literal, a variant or
  an array left the owned operands before it undestroyed on its error
  path, and after an argument that edits a value with a destructor it
  was an internal error of the compiler.
- Fixed: assignment through an access, to a field or to an element did
  not destroy the previous value; a temporary used as a method receiver
  was never destroyed. Programs that relied on neither are unchanged;
  programs that leaked no longer do.

### Standard library

- `std/io`: `File` with `OpenOptions`, `seek`, `sync`, `set_len` and
  `metadata`; `metadata`, `symlink_metadata`, `exists`, `rename`,
  `hard_link`, `symlink`, `read_link`, `set_mode`, `canonicalize`,
  `current_dir`, `copy_file`; the interfaces `Reader` and `Writer` with
  `BufReader` (lines with either terminator) and `BufWriter`; `stdin()`,
  `stdout()`, `stderr()`; `ErrKind` and `kind(e)`, the same for the same
  failure on every host; `flush_stdout`, `is_terminal`. `FileKind` gained
  `Symlink`: an exhaustive `match` on it needs the new arm.
- `std/net` (new): IPv4 and IPv6 addresses, TCP listeners and streams,
  UDP sockets, Unix-domain listeners, streams and pairs, with a timed
  form of every call that waits.
- `std/io`: `read_dir` (entries with their kinds), `create_dir_all`,
  `remove_dir_all`, `join`; `File.lock`, `try_lock`, `unlock` (advisory
  locks), `File.raw_fd`.
- `std/mem`: `map_file` and `Mapping`, a file's bytes in memory.
- `std/io`: `File.lock_range`, `try_lock_range` and `unlock_range`,
  advisory locks on ranges of a file's bytes.
- `std/net`: `SocketAddr` has a `scope` (the interface of an IPv6
  link-local address), read and written as `[v6%zone]:port`, with
  `addr_scoped`, `interface_index` and `interface_name`; `UnixDatagram`,
  datagrams between programs on one machine; socket options as methods
  (`set_keepalive`, `set_recv_buffer`, `set_send_buffer`,
  `set_hop_limit`, `set_linger`, `set_broadcast`, each with its reading
  form, and `nodelay`).
- `signal.raise` flushes standard output before it sends the signal.
- `std/net`: `Poller`, the system's readiness queue (epoll, kqueue): one
  thread waits for many sockets and serves those that are ready.
  `UnixStream.send_with`, `send_file` and `recv_with` pass open sockets
  and files between processes; a received `Descriptor` becomes the type
  the program names, checked against what the system says it is; a
  descriptor the program does not take is closed, on macOS too, where
  the system would leave it open in the receiving process.
  `UnixStream.peer_credentials`. On Linux, a Unix-domain path that
  begins with a zero byte is a name in the abstract namespace.
- `std/sync` (new): `RwLock`, `Once`, `WaitGroup`, a bounded `Channel`.
- `std/signal` (new): `catch`, `ignore`, `reset`, `raise`, and `Signals`
  to wait for what was caught.
- `std/rand` (new): the system's generator, and `Rng`, a seeded generator
  with `below`, `fill` and `shuffle`.
- `std/mem` (new): `page_size` and `PageAllocator`.
- Prelude: `park`, `park_ns`, `unpark_one`, `unpark_all`;
  `Condvar.wait_ns`; `Mutex.try_with`; `thread.set_stack_bytes`,
  `stack_bytes`, `available_parallelism`, `current_id`.
- `process.Pipe` is an `io.Reader` and an `io.Writer`.
- A spawn that cannot allocate what the thread needs is
  `Err(SpawnErr)`; it trapped `oom`.
- A new file's mode is 0666 less the umask; it was 0644 less the umask.

### Runtimes and targets

- A trap's record carries the line and column of its span. With
  `KERN_BACKTRACE=1` or a build made with `--backtrace`, a second line
  lists the kern functions on the trapping thread's stack.
- Standard output is flushed after each line on a terminal and before a
  read of standard input; a failed write is reported by `flush_stdout`.
- A debug build traps `explicit` on a `free` or `realloc` that names no
  live block or the wrong size, and with `KERN_ALLOC_REPORT=1` reports
  its allocations when the program ends.
- A thread destroyed without `join` destroys its result and frees its
  record when it ends; it leaked both.
- `--stack-bytes=N` (`stack_bytes` in `kern.mod`) and `KERN_STACK_BYTES`
  give spawned threads and `main` a stack of N bytes.
- The freestanding runtime's devices are a board file's
  (`kern_board.h`): `--board`, `--linker-script`, `--startup` and
  `--heap-bytes` (`board`, `linker_script`, `startup`, `heap_bytes` in
  `kern.mod`), and `--stack-bytes` sizes its stack. Its allocator traps
  on a double free.
- The link drops the runtime code a program does not reach.
- The runtime's interface has a version (`kern_rt_abi_2`); a program and
  a runtime of different versions do not link.

### Tooling

- `kern run`: a signal left at its default ends an interpreted program by
  that signal, as it ends a native one (`User1`, `User2` and `Quit` did
  not); a mapped file is the system's mapping, shared with the file's
  other users; a fault in mapped memory is a trap of kind `unreachable`.
- `kern run`: `io.list_dir` lists a directory whose entries the system
  will not describe one by one (`/dev/fd` on macOS 26); the listing
  failed.
- `docs/runtime.md`, the runtime's reference, with every entry of both
  runtimes generated from their headers; `docs/freestanding-porting.md`.
- The fixture corpus runs under AddressSanitizer and
  UndefinedBehaviorSanitizer on macOS and Linux.

## [v0.1.0] - 2026-11-08

The first release: a research preview of the language, its compiler, its
standard library and its tools, as they stand after the six implementation
phases of `specs/initial/PLAN.md` and the work recorded in `docs/reports/`
since. The engineering reports, the specifications under `specs/` and the
ADRs are the detailed record; this section summarizes them.

### Language

- Safe code has no first-class references: access is second-class, through
  `view`/`edit`/`consume`/`out` parameter conventions and lexically scoped
  `with` bindings (`docs/ownership.md`, ADR-001, ADR-005).
- Static moves, single-source elision for returned accesses, and checked
  multi-access functions such as `get2` (ADR-003, ADR-004).
- Two-phase `edit` arguments: a call such as `v.push(v.get(0usize))`, whose
  other arguments only read the edited place, is accepted (ADR-016,
  `docs/reports/two-phase-edit.md`).
- Arenas, pools and generation-checked handles as library types (ADR-002,
  ADR-014).
- Monomorphized generics with interfaces and `where` clauses, function
  pointers and non-escaping closures (ADR-009, ADR-010,
  `docs/reports/closures.md`).
- `for x in xs`, `for i, x in xs`, and `for k, v in m` over maps through a
  cursor protocol (`docs/reports/for-index.md`, `docs/reports/for-cursor.md`).
- Traps instead of panics, checks on by default, and the trap kinds a
  function may raise visible to callers (ADR-006, ADR-008,
  `docs/reports/effect-trap-kinds.md`).
- Threads with `Send`/`Share` and scoped spawn, atomics and a memory model
  restricted from C++20 (ADR-007, `spec/memory-model.md`).
- Declared budgets: a function may state how many runtime bounds checks,
  deep copies and allocations it allows, and `check` and `build` enforce
  them (`docs/reports/declared-budgets.md`).
- Multi-module programs found from their imports, with an optional
  `kern.mod` manifest (`docs/reports/module-discovery.md`).
- The grammar, type system, memory model and formatting rules are in
  `spec/`; `spec/rules.lock` freezes every diagnostic code to its rule
  number.

### Compiler and analyses

- A Go compiler that emits LLVM IR, built and linked through clang, and a
  reference interpreter that is the backend's behavioural oracle; the test
  suite runs every fixture in both (`docs/reports/phase-2.md`,
  `docs/reports/program-differential.md`).
- An ownership checker with one diagnostic code per rule, and
  machine-applicable fixes for the mechanical diagnostics
  (`docs/reports/diagnostic-fixes.md`).
- A kern-level inliner and a proof pass that removes bounds checks it can
  prove, including guards through joins and loops, masks, remainders,
  non-strict guards and slice bounds (`docs/reports/inliner.md`,
  `docs/reports/prove-guards.md`, `docs/reports/prove-masks.md`,
  `docs/reports/prove-nonstrict.md`).
- For each runtime-checked bounds check, a reason it is not proven and a
  hint naming a rewrite the proof rules accept
  (`docs/reports/unproven-reasons.md`).
- DWARF line tables and `kern debug`, an lldb session with ownership state
  at the current line (`docs/reports/phase-5.md`).
- Native traps and faults report the kern function, file and span on Linux
  and macOS (`docs/reports/native-trap-loc.md`,
  `docs/reports/runtime-trap-file.md`).

### Standard library

- The prelude, `std/core.kern`: `Box`, `Vec`, `String`, `Pool`, `Arena`,
  `Shared`, `Mutex`, `Condvar`, `Atomic`, scoped threads and the raw-pointer
  core, written in kern over audited `unsafe` blocks
  (`docs/reports/containers-in-kern.md`, `docs/unsafe-audit.md`).
- Fallible allocation: every allocating operation has a `try_` form that
  returns `Err(OutOfMemory)`, and the effect analysis shows which code cannot
  trap `oom` (`docs/reports/fallible-alloc.md`,
  `docs/reports/fallible-runtime-objects.md`).
- Containers over a caller's allocator: `BoxIn`, `VecIn`, `ArenaIn`,
  `PoolIn` and `map.MapIn` (`docs/reports/alloc-containers.md`,
  `docs/reports/alloc-containers-more.md`, `docs/reports/map-in.md`,
  `docs/reports/in-forms.md`).
- In-place initialization: `new_with`, `alloc_with` and their `_ctx` forms
  (`docs/reports/in-place-init.md`, `docs/reports/init-context.md`).
- Imported modules: `std/str`, `std/map`, `std/io`, `std/json`,
  `std/process` and `std/time` (`docs/reports/std-library.md`,
  `docs/reports/std-gaps.md`, `docs/reports/std-files.md`,
  `docs/reports/std-process.md`, `docs/reports/std-time.md`,
  `docs/reports/std-timeouts.md`, `docs/reports/float-text.md`).

### Runtimes and targets

- Hosted: Linux (glibc 2.34 or newer) and macOS, on x86-64 and arm64, with a
  small C runtime compiled by clang and cached between builds
  (`runtime/hosted/`, `docs/reports/build-time.md`).
- Freestanding: bare-metal x86-64 and AArch64 images that boot under QEMU,
  with the library containers, floating-point text and a monotonic clock;
  using a hosted-only facility is the diagnostic `ENV_HOSTED_ONLY`
  (ADR-011, ADR-012, ADR-015, `docs/reports/freestanding-availability.md`,
  `docs/reports/freestanding-float.md`, `docs/reports/freestanding-clock.md`).
- Hosted kern functions keep the frame pointer; the measured cost is nil on
  arm64 macOS and about 1% on x86-64 under Rosetta
  (`docs/reports/x86-frame-pointers.md`).

### Tooling

- `kern version` and `kern --version` report the version, commit, Go
  toolchain, platform and the machine-API schema versions the binary answers
  (`specs/release-v0.1/SPEC.md`).
- The machine API: one JSON request and response per command, at
  `schema_version` 0.5, with 0.4, 0.3 and 0.2 requests answered in their own
  shapes (`tools/agent-api/schema.md`).
- `kern test`: `test_` functions in `*_test.kern` files, run in the
  interpreter and natively, at debug and release, with the four runs
  compared (`docs/reports/test-command.md`, `docs/reports/test-runs.md`).
- `kern lsp`: diagnostics, formatting, quick fixes, hover, definition,
  references, rename, completion, outline and signature help
  (`docs/reports/language-server.md`, `docs/editors.md`).
- `kern format`, idempotent and diagnostic-preserving (`spec/format.md`).
- Reproducible release builds for Linux and macOS on both architectures,
  with `SHA256SUMS`, published by `.github/workflows/release.yml` when a tag
  is pushed (`scripts/release.sh`, `docs/releasing.md`).

### Performance

- In the latest harness measurements, kern meets its throughput target (at
  least 0.90 of the C or Rust reference row) on all thirteen benchmark
  workloads and its memory target (at most 1.10) on twelve; btree uses 1.23
  times the memory, the width of its checked handles
  (`docs/reports/quiet-host-2026-10-31.md`, `bench/PROTOCOL.md`).
- The interpreter compiles instructions into ops at a function's first call
  and avoids most per-call allocations (`docs/reports/interp-compile.md`,
  `docs/reports/interp-call-path.md`, `docs/reports/interp-alloc.md`).

### Known limitations

- The compiler is written in Go; self-hosting is deferred
  (ADR-013). A kern lexer and parser written in kern exist as examples
  (`examples/selfhost`, `examples/selfhost_parser`).
- No Windows support; native builds need clang, and freestanding images
  need `ld.lld` and QEMU.
- Releases are checksummed, not signed (`docs/security-review.md`).
- The machine API is a local tool with the user's permissions: paths are
  opened as given and inputs have no size limits
  (`docs/security-review.md`).
- The agent experiment has not met its own threshold, and its fourth task
  suite has not been run (`bench/agent/REPORT.md`,
  `docs/reports/agent-tasks-v4.md`).
- The interpreter boxes integers of 256 and above, its largest remaining
  cost (`docs/reports/interp-alloc.md`).

### Stability

This is a 0.x release. A later minor version may change the language, the
standard library, the CLI and the machine API; a patch version does not
intend to. Diagnostic codes stay frozen to their rule numbers in
`spec/rules.lock`, the `schema_versions` list of `kern version` names the
machine-API versions a release answers, and any change to either is listed
here.

[Unreleased]: https://github.com/dshills/kern/compare/v0.2.3...HEAD
[v0.2.3]: https://github.com/dshills/kern/releases/tag/v0.2.3
[v0.2.2]: https://github.com/dshills/kern/releases/tag/v0.2.2
[v0.2.1]: https://github.com/dshills/kern/releases/tag/v0.2.1
[v0.2.0]: https://github.com/dshills/kern/releases/tag/v0.2.0
[v0.1.0]: https://github.com/dshills/kern/releases/tag/v0.1.0
