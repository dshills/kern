# kern

kern is an experimental, AI-first systems programming language with a Go-inspired
surface, Rust-class memory and concurrency safety for safe code, no garbage
collector and a runtime small enough for freestanding targets. The compiler is
written in Go and emits LLVM IR; programs run natively through clang, or in a
reference interpreter that serves as the behavioural oracle for the backend.

The central design decision: **safe code has no first-class references.**
Temporary access to a value is second-class (a parameter convention or a
lexically scoped `with` binding) and can never be stored, captured or returned.
That removes lifetime annotations by removing the values that would need them.
Arenas, pools, handles and synchronisation are library types built on one
ownership mechanism, not type-system features.

This repository is a research implementation. The specification is a v1.0
candidate; the standard library covers the thirteen benchmark workloads plus
strings, hash maps, file I/O, JSON and child processes, every library
prerequisite of self-hosting (`docs/decisions/ADR-013-self-hosting.md`).

## A taste

```text
module vector

struct Stats { count: usize; total: u64; max: u64 }

func stats(v: view[Vec[u64]]) -> Stats {
    s := Stats { count: v.len(), total: 0u64, max: 0u64 }
    for x in v.as_view() {
        s.total = s.total + x
        if x > s.max { s.max = x }
    }
    return s
}

func double_all(v: edit[Vec[u64]]) {
    n := v.len()
    i := 0usize
    while i < n {
        with e := v.at(i) { e = e * 2u64 }
        i = i + 1usize
    }
}

func main() -> u64 {
    v := Vec[u64].new()
    v.push(1u64)
    v.push(2u64)
    double_all(v)
    s := stats(v)
    return s.total + u64(s.count) + s.max
}
```

`view` and `edit` are the two access conventions; `own` moves. Integer
arithmetic traps on overflow in every build, every index is bounds-checked
unless the compiler records a proof that removes the check, and a trap is
observable only as termination with a structured `TrapInfo` record. Errors a
program is expected to handle are values instead: running out of memory is
`Err(OutOfMemory)` from the allocator, from `Pool` and `Arena`, and from the
`try_` forms of `Box`, `Vec` and `String` (`try_new`, `try_reserve`,
`try_push`, `try_extend`, ...), and an append into reserved capacity never
allocates, so code that must survive allocation failure has no trap path for
it (`docs/ownership.md` rule 5.6.4).

## What is here

| Area | Where | Notes |
|---|---|---|
| Specification | `specs/initial/AI_Native_Systems_Language_SPEC.md`, `spec/` | Grammar (`grammar.ebnf`), type system, memory model, formatting, and `rules.lock` freezing every diagnostic code to its rule number |
| Language reference | `docs/ownership.md` | Normative restatement of the ownership rules, one rule per diagnostic code |
| Decisions | `docs/decisions/ADR-*.md` | Sixteen architecture decision records with the alternatives considered |
| Compiler | `compiler/` | Lexer and parser, resolver, ownership checker, lowering to a semantic IR, the kern-level inliner and the bounds-check proof pass, the analyses behind the `show-*` commands and declared budgets, LLVM text backend, interpreter, formatter, incremental driver, language server |
| Standard library | `std/core.kern`, `std/str.kern`, `std/map.kern`, `std/io.kern`, `std/json.kern`, `std/process.kern`, `std/time.kern`, `std/sync.kern`, `std/mem.kern`, `std/net.kern`, `std/signal.kern`, `std/rand.kern` | The implicitly imported prelude (`Box`, `Vec`, `Pool`, `Arena`, `Shared`, `Mutex` (with `try_with`), `Condvar` (with a timed `wait_ns`), `Atomic`, `park`, scoped threads, raw pointers; `BoxIn`, `VecIn`, `ArenaIn` and `PoolIn` over a caller's allocator; in-place initializers `new_with` and `alloc_with`; the containers are written in kern over audited `unsafe` blocks, `docs/unsafe-audit.md`, and every allocating operation has a `try_` form that returns `Err(OutOfMemory)` where the convenience form traps, `docs/reports/fallible-alloc.md`), and the modules a program imports as `std/str` (an owned `String` with decimal, signed, hexadecimal, padded and round-trip floating-point formatting, `parse_f64`, fallible `try_` appends, byte-view helpers, and UTF-8 decoding, validation, counting and encoding), `std/map` (a hash map over `Hashable` keys, with lookups that borrow the key, lookup of `String` keys by bytes, `each`/`each_mut` visits by closure, `for k, v in m` through the cursor protocol, `reserve`/`try_reserve`, and `MapIn` over a caller's allocator), `std/io` (`File` with `OpenOptions`, seeking, `sync` and `set_len`; metadata, links, renames, `canonicalize` and `copy_file`; advisory file locks on a whole file or a range of its bytes; directories (`read_dir`, `create_dir_all`, `remove_dir_all`) and temporary files; the `Reader` and `Writer` interfaces with `BufReader`, line reading and `BufWriter`; the standard streams and arguments; `ErrKind`, the same for the same failure on every host; hosted only, `docs/reports/runtime-files.md`), `std/json` (JSON values, a parser that keeps numbers' text and rejects invalid UTF-8, numbers read and written as integers or floats, and a writer), `std/process` (child processes started with arguments, environment edits, a working directory and inherited, `/dev/null` or piped streams; waiting, polling and signals; `output` with no deadlock for any size; the environment the program started with; hosted only, `docs/reports/std-process.md`), `std/time` (a monotonic clock, durations, deadlines, sleep and the wall clock; hosted only, `docs/reports/std-time.md`), `std/sync` (`RwLock`, `Once`, `WaitGroup` and a bounded `Channel`, written in kern over the prelude's `park` and `Atomic`; hosted only, `docs/reports/runtime-sync.md`), `std/mem` (the page size and `PageAllocator`, which maps each allocation from the operating system, and `map_file`, a file's bytes in memory; hosted only, `docs/reports/runtime-memory.md`) and `std/net` (IPv4 and IPv6 addresses with parsing, canonical formatting and the system resolver; scoped link-local addresses; `TcpListener`, `TcpStream`, `UdpSocket` and Unix-domain `UnixListener`, `UnixStream` and `UnixDatagram`, with timed forms of every call that waits and the socket options set and read by methods; `Poller`, with which one thread serves many sockets; open files and sockets passed between processes over a `UnixStream`, and its peer's credentials; streams are `io.Reader` and `io.Writer`; hosted only, `docs/reports/runtime-net.md`), `std/signal` (signals caught, ignored, reset and raised; a caught signal is recorded and handed over by `wait`, so no handler runs at delivery; hosted only) and `std/rand` (the system's generator behind `fill`, `u64` and `below`, hosted only; `Rng`, a seeded generator whose sequence is the same everywhere, with `below`, `fill` and `shuffle`, usable on bare metal; `docs/reports/runtime-signal-rand.md`) |
| Runtimes | `runtime/hosted/`, `runtime/freestanding/` | A small C runtime for Linux/macOS, and a freestanding one for bare-metal x86-64 and AArch64 images that boot under QEMU |
| Machine API | `tools/agent-api/schema.{json,md}` | JSON request/response schema for every command, with stable diagnostic codes and machine-applicable fixes |
| Tests | `tests/` | Positive, negative and adversarial fixtures (one rejecting fixture and one accepting twin per rule), trap fixtures, concurrency and FFI fixtures, freestanding images |
| Benchmarks | `bench/` | Thirteen workloads in C, Rust and kern; `bench/PROTOCOL.md` is the method and `bench/REPORT.md` the latest run |
| Examples | `examples/` | Hello, vector, B-tree, arena-allocated AST, packet parser, thread hand-off, a module with its tests (`examples/testing`), a kern lexer and a kern parser written in kern, and five applications on the runtime, each a working tool that with no arguments runs a demonstration of itself: `kq` (queries over JSON, in the manner of jq), `kvstore` (a key-value server on one thread and a readiness queue, with a log it replays), `ksearch` (a pattern search over directory trees on every processor, through mapped files), `supervise` (starts, restarts and stops the programs a configuration names) and `httpd` (a static file server with a pool of threads); and five more that lean on other parts of the language and library: `klisp` (a Lisp with a collected heap, in a module of its own with tests), `ksort` (a merge sort of files larger than the memory it is allowed, through an allocator that counts), `kzip` (a gzip reader and writer whose inner loops declare that they allocate nothing), `kcache` (a cache with expiry and eviction behind reader-writer locks, served over UDP) and `kray` (a ray tracer on SIMD lanes that renders on every processor) |
| Reports | `docs/reports/`, `docs/security-review.md` | One engineering report per phase and per change since, with its measurements; the security review |
| Runtime reference | `docs/runtime.md`, `docs/freestanding-porting.md`, `docs/decisions/ADR-017-runtime-architecture.md` | What runs under a program: startup and exit, the standard streams, traps and backtraces, threads, memory checks, every environment variable and build option, and every runtime entry (generated from the headers); how to bring the freestanding runtime to a board; why the runtime has the shape it has (`docs/reports/runtime-summary.md`) |
| Releases | `CHANGELOG.md`, `docs/releasing.md` | What each release contains and how one is made |

## Installing

The current release is **v0.2.1** (`CHANGELOG.md`). The `kern` binary is
self-contained: the standard library and both runtimes are built into it.
Native builds need `clang` (the hosted runtime is compiled and linked with
it; the compiled runtime is kept under the user cache directory,
`kern/runtime`, one object per configuration, and `KERN_NO_RUNTIME_CACHE=1`
turns that off) and, on Linux, glibc 2.34 or newer (`std/process` starts
children with its `posix_spawn_file_actions_addclosefrom_np`); freestanding
images need `ld.lld`, and running them needs `qemu-system-x86_64` /
`qemu-system-aarch64`. The interpreter needs nothing else. Linux and macOS
are supported, on x86-64 and arm64; Windows is not.

**From a release archive.** Download the archive for your platform and
`SHA256SUMS` from the
[releases page](https://github.com/dshills/kern/releases), check it, unpack
it, and put `kern` on your `PATH`:

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
```

```bash
tar -xzf kern-v0.2.1-darwin-arm64.tar.gz && sudo install kern-v0.2.1-darwin-arm64/kern /usr/local/bin/
```

**With Go** (1.25 or newer):

```bash
go install github.com/dshills/kern/cmd/kern@latest
```

**From source**, in a clone of this repository:

```bash
go build ./cmd/kern
```

`kern --version` prints the version, commit, Go toolchain and platform;
`kern version` gives the same as one JSON response, with the machine-API
schema versions the binary answers.

## Building

The examples below run in a clone of this repository, with `kern` on your
`PATH` (or as `./kern` after building from source). Run a program natively
(defaults to the debug configuration):

```bash
kern run examples/hello/hello.kern
```

which prints `hello, kern`. A program's `main` may return an integer, which
becomes the exit status: `kern run examples/vector/vector.kern` prints
nothing and exits with its result, 21.

Build a release binary:

```bash
kern build --opt=release -o vector examples/vector/vector.kern
```

Name only the entry file: the modules it imports are found under the project
root. `import "lib"` is the `.kern` files of `<root>/lib/`, each declaring
`module lib`; the root is the nearest ancestor directory of the entry file
holding a file named `kern.mod`, else the working directory. `kern.mod` may be
empty, or hold a JSON object with `entry` (the file or files to start from),
`opt`, `env`, `target` and `output`, so that a project runs and builds with no
arguments; a flag always overrides the manifest. For a two-module program:

```text
proj/kern.mod            {"entry": "app.kern", "output": "app"}
proj/app.kern            module app  /  import "lib"  /  ... lib.Twice(21u64) ...
proj/lib/lib.kern        module lib  /  func Twice(x: u64) -> u64 { return x * 2u64 }
```

```bash
cd proj && kern run && kern build
```

Check a file and read the diagnostics as text rather than JSON:

```bash
kern check --human examples/btree/btree.kern
```

## Commands

Every command other than `run`, `build`, `debug`, `test`, `lsp` and `version` reads one JSON request on
standard input (or takes files on the command line) and writes one JSON response
on standard output; `--human` renders the response as text. The schema is in
`tools/agent-api/schema.md`. A request names its `schema_version`: 0.5, the
current one, whose `show-bounds-checks` entries say why each runtime-checked
bounds check is not proven (`reason`, and a `hint` naming a rewrite the proof
rules accept); 0.4, answered without `reason` and `hint`, whose `show-effects`
entries list the trap kinds a function may raise (`trap_kinds`, so a caller
can check that code cannot trap `oom`); 0.3, answered also without
`trap_kinds`; or 0.2, answered also without the diagnostics' `message`.
Files on the command line get 0.5.

| Command | What it does |
|---|---|
| `check` | Parse, resolve, type-check and ownership-check, the program's test files (`*_test.kern`) included; diagnostics carry a code, a rule number, a one-sentence message and, where one exists, a machine-applicable fix |
| `format` | Canonical formatting (`spec/format.md`): idempotent and diagnostic-preserving |
| `emit-ir`, `emit-asm` | The semantic IR, or the LLVM IR the backend produces |
| `explain-ownership` | For a span or `--line=N`: each place's ownership state, drop point and accesses |
| `show-layout` | Exact sizes, alignments and field offsets for the target |
| `show-copies`, `show-allocations`, `show-bounds-checks`, `show-effects` | Where deep copies, heap allocations, runtime bounds checks and side effects occur, each labelled `proven`, `runtime-checked`, `assumed-unsafe`, `estimated` or `unknown` |
| `apply-structured-edit` | Apply a semantic edit from the machine API and return the new source |
| `run` | Execute in the reference interpreter; `--opt=debug\|release`, `--env=hosted\|freestanding`, `--stack-bytes=N` (65,536 to 1,073,741,824), `--backtrace`, `--test-hooks` |
| `build` | Produce an executable or freestanding image; `--debug` emits DWARF line tables; `--stack-bytes=N` (or `stack_bytes` in `kern.mod`) gives spawned threads and `main` a stack of N bytes, which `KERN_STACK_BYTES` overrides when the program runs; `--backtrace` makes a trap list the functions on the stack after its record (`KERN_BACKTRACE=1` or `0` decides it at run time); a debug build checks every `free` and `realloc` of the system allocator against its table of live blocks, and run with `KERN_ALLOC_REPORT=1` it reports its allocations when the program ends; a freestanding build takes `--board=<file.c>` (a board file in place of the QEMU default), `--linker-script=<file.ld>`, `--startup=<file.S>`, `--heap-bytes=N` and `--stack-bytes=N` for its one stack (also the `kern.mod` keys `board`, `linker_script`, `startup`, `heap_bytes`, `stack_bytes`) |
| `debug` | Build with debug info and launch lldb with a `kern-own` command that shows ownership state at the current line |
| `test` | Run the program's tests in the interpreter and natively, in debug and release, and compare the runs; `--opt=`, `--engine=interp\|native`, `--run=<regexp>`, `--timeout=<seconds>`, `--jobs=<n>`, `--human` |
| `lsp` | A language server on standard input and output: diagnostics as you type, formatting, the compiler's fixes as quick fixes, ownership facts on hover, go to definition, find references, rename, completion, an outline and signature help; setup for Neovim, Helix and Emacs is in [docs/editors.md](docs/editors.md) |
| `version` | The version, commit, Go toolchain, platform and the machine-API schema versions this binary answers, as one JSON response; `--human` (or `kern --version`) prints one line |

### Tests

A test is a `test_` function in a file named `*_test.kern`, in the module it
tests; `expect(cond)` traps `explicit` when `cond` is false (`expect_msg(cond,
"why")` also reports the message), a `//! expect: trap <kind>` line above a
test says it must trap with that kind, and a test file's `setup` and
`teardown` functions run before each of its tests and after each one that
returns:

```kern
// stack/stack_test.kern
module stack
func test_push_pop() {
    s := New()
    s.Push(7u64)
    expect(s.Pop() == 7u64)
}
//! expect: trap bounds
func test_push_full() {
    s := New()
    var i: u64 = 0u64
    while i < 5u64 { s.Push(i); i = i + 1u64 }
}
```

`kern test` (from `examples/testing`, or with the entry file named) runs each
test in four configurations, each run from a fresh program state, and
writes one JSON response: per test its status (`pass`, `fail`, `mismatch`
when two configurations disagree, `timeout`, `skipped`) and every run's
outcome, trap record and output. It exits 0 when every test passes and 1
otherwise. Runs proceed in parallel, `--jobs` at a time, and a run that
exceeds `--timeout` is stopped, threads blocked in locks included. `kern check` loads the test files too and reports their
errors; the other commands leave `*_test.kern` files out of a module.
`specs/test-command/SPEC.md` is the full contract.

## Testing and benchmarks

```bash
go test ./...
```

The fixture suite runs every program in both the interpreter and the native
backend and requires them to agree; three differential tests generate
random programs (indexing under the proof pass's guards, integer
arithmetic near the type limits, and a mix of the whole language with the
`std/str` and `std/map` code) and require the same agreement, and two more
compare `std/json` with Go's `encoding/json` and `std/str`'s UTF-8
functions with Go's `unicode/utf8` over generated inputs. Freestanding fixtures run under QEMU when it
is installed and are skipped otherwise; `KERN_REQUIRE_QEMU=1` (set in CI) turns
the skip into a failure. `golangci-lint run ./...` is part of CI, which runs on
Ubuntu x86-64, Ubuntu arm64 and macOS (`.github/workflows/ci.yml`).

```bash
go run ./bench/harness
```

regenerates `bench/REPORT.md` and checks that the C, Rust and kern
implementations agree on their checksums. It measures each row in ten
interleaved rounds and compares kern with its reference row as a paired ratio
with a 95% interval; `-max-load` waits for a quiet host before each round.

In the latest measurements (2026-10-31, `docs/reports/quiet-host-2026-10-31.md`),
user-level kern code meets the 10% throughput target on all thirteen workloads:
four rows needed more rounds than the standard ten to settle (30, and 60 for
btree). Peak memory meets the target on twelve of the thirteen; btree uses 1.23×
the memory of C, the width of its checked 8-byte handles (ADR-014). kern's p99
latency is within the interval of the better of C and Rust on every workload.

## Status

v0.2.1 packages the state described here: v0.1.0, the first release, the
runtime work of v0.2.0 (`specs/runtime`,
`docs/reports/runtime-summary.md`), and what writing programs on that
runtime found and asked for (`docs/reports/reference-conformance.md`,
`docs/reports/std-streams.md`). Each release's changelog entry
(`CHANGELOG.md`) summarizes it by area, and v0.1.0's lists the known
limitations. The implementation went through five planned phases (`specs/initial/PLAN.md`,
reports in `docs/reports/`): workload design, the core language and
interpreter, the native backend and ownership checker, concurrency and
freestanding targets, tooling and the machine API, and hardening (frozen
rules, DWARF, reproducible releases, security review), then a sixth of
performance work and language additions (kern-level inlining, condition
variables, byte-order reads and writes, `where` clauses, non-escaping
closures, `std/str`, `std/map` and `std/io`; `docs/reports/`). The agent
experiment's full matrix has been run on two task suites. On the first
(`bench/agent/tasks-v1/`) every trial succeeds in both arms and the tasks
do not separate them. On the second (`bench/agent/tasks-v2/`,
`docs/reports/agent-tasks-v2.md`, results in `bench/agent/REPORT.md`),
whose requirements are stated in terms of what the compiler's analyses
report, the structured arm succeeds in all 100 trials and the plain arm in
90, failing where a requirement needs a count the text diagnostics do not
carry; the queries cost iterations elsewhere. The primary metric, revised
on 2026-10-28 to weigh successes before median iterations
(`docs/reports/agent-metric-revision.md`), records 0 wins of 10 on the
first suite and 1 on the second, below its threshold of 8. A third suite
(`bench/agent/tasks-v3/`, `docs/reports/agent-tasks-v3.md`), built entirely on
the one requirement the plain arm failed, a bound on runtime-checked bounds
checks, was written but never run, and the proof pass has since proved most
of its idioms. A fourth suite (`bench/agent/tasks/`,
`docs/reports/agent-tasks-v4.md`) is built on the indexing idioms the proof
pass still leaves checked; it is written and validated offline, each task
against a natural solution that must fail on the budget alone, and has not
been run. Open items are
recorded in the reports and ADRs rather than in an issue tracker.

Specification documents, the language reference and the ADRs are the
authority; where the code disagrees with them, the code is wrong.

## Versioning and stability

Releases are tagged `vMAJOR.MINOR.PATCH` and follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) in its 0.x form:

- A minor version (0.1 → 0.2) may change the language, the standard
  library, the CLI and the machine API. A patch version (0.1.0 → 0.1.1)
  does not intend to. `CHANGELOG.md` lists what changed.
- Diagnostic codes are frozen to rule numbers in `spec/rules.lock` (checked
  by `tests/rules_lock_test.go`). A code is never reused for another rule,
  and any change to the file is listed in the changelog.
- The machine-API versions a release answers are the `schema_versions` list
  in the `kern version` result; a request at any of them is answered in
  that version's shape. A release that drops one says so in the changelog.
- The language specification is a v1.0 candidate and may still change.

Making a release is described in `docs/releasing.md`.

## License

MIT. See [LICENSE](LICENSE).
