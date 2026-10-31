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
strings, hash maps and file I/O (no JSON or process control yet; see
`docs/decisions/ADR-013-self-hosting.md`).

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
observable only as termination with a structured `TrapInfo` record.

## What is here

| Area | Where | Notes |
|---|---|---|
| Specification | `specs/initial/AI_Native_Systems_Language_SPEC.md`, `spec/` | Grammar (`grammar.ebnf`), type system, memory model, formatting, and `rules.lock` freezing every diagnostic code to its rule number |
| Language reference | `docs/ownership.md` | Normative restatement of the ownership rules, one rule per diagnostic code |
| Decisions | `docs/decisions/ADR-*.md` | Fourteen architecture decision records with the alternatives considered |
| Compiler | `compiler/` | Lexer and parser, resolver, ownership checker, lowering to a semantic IR, LLVM text backend, interpreter, formatter, incremental driver |
| Standard library | `std/core.kern`, `std/str.kern`, `std/map.kern`, `std/io.kern` | The implicitly imported prelude (`Box`, `Vec`, `Pool`, `Arena`, `Mutex`, `Condvar`, `Atomic`, scoped threads, raw pointers; the containers are written in kern over audited `unsafe` blocks, `docs/unsafe-audit.md`), and the modules a program imports as `std/str` (an owned `String` with decimal, signed, hexadecimal and padded formatting, byte-view helpers, and UTF-8 decoding, validation, counting and encoding), `std/map` (a hash map over `Hashable` keys, with lookups that borrow the key, lookup of `String` keys by bytes, and `each`/`each_mut` visits by closure) and `std/io` (files, the standard streams and arguments; hosted only) |
| Runtimes | `runtime/hosted/`, `runtime/freestanding/` | A small C runtime for Linux/macOS, and a freestanding one for bare-metal x86-64 and AArch64 images that boot under QEMU |
| Machine API | `tools/agent-api/schema.{json,md}` | JSON request/response schema for every command, with stable diagnostic codes and machine-applicable fixes |
| Tests | `tests/` | Positive, negative and adversarial fixtures (one rejecting fixture and one accepting twin per rule), trap fixtures, concurrency and FFI fixtures, freestanding images |
| Benchmarks | `bench/` | Thirteen workloads in C, Rust and kern; `bench/PROTOCOL.md` is the method and `bench/REPORT.md` the latest run |
| Examples | `examples/` | Vector, B-tree, arena-allocated AST, packet parser, thread hand-off, and a kern lexer written in kern |
| Reports | `docs/reports/` | Per-phase engineering reports and the security review |

## Building

Requirements: Go 1.25 or newer. Native builds additionally need `clang`
(the hosted runtime is compiled and linked with it); freestanding images need
`ld.lld`, and running them needs `qemu-system-x86_64` / `qemu-system-aarch64`.
The interpreter needs nothing beyond Go.

```bash
go build ./cmd/kern
```

Run a program natively (defaults to the debug configuration):

```bash
./kern run examples/vector/vector.kern
```

Build a release binary:

```bash
./kern build --opt=release -o vector examples/vector/vector.kern
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
./kern check --human examples/btree/btree.kern
```

## Commands

Every command other than `run`, `build`, `debug`, `test` and `lsp` reads one JSON request on
standard input (or takes files on the command line) and writes one JSON response
on standard output; `--human` renders the response as text. The schema is in
`tools/agent-api/schema.md`. A request names its `schema_version`: 0.3, the
current one, whose diagnostics carry a `message`, or 0.2, answered in the 0.2
shape without it; files on the command line get 0.3.

| Command | What it does |
|---|---|
| `check` | Parse, resolve, type-check and ownership-check, the program's test files (`*_test.kern`) included; diagnostics carry a code, a rule number, a one-sentence message and, where one exists, a machine-applicable fix |
| `format` | Canonical formatting (`spec/format.md`): idempotent and diagnostic-preserving |
| `emit-ir`, `emit-asm` | The semantic IR, or the LLVM IR the backend produces |
| `explain-ownership` | For a span or `--line=N`: each place's ownership state, drop point and accesses |
| `show-layout` | Exact sizes, alignments and field offsets for the target |
| `show-copies`, `show-allocations`, `show-bounds-checks`, `show-effects` | Where deep copies, heap allocations, runtime bounds checks and side effects occur, each labelled `proven`, `runtime-checked`, `assumed-unsafe`, `estimated` or `unknown` |
| `apply-structured-edit` | Apply a semantic edit from the machine API and return the new source |
| `run` | Build and execute natively; `--opt=debug\|release`, `--env=hosted\|freestanding`, `--stack-bytes=N`, `--test-hooks` |
| `build` | Produce an executable or freestanding image; `--debug` emits DWARF line tables |
| `debug` | Build with debug info and launch lldb with a `kern-own` command that shows ownership state at the current line |
| `test` | Run the program's tests in the interpreter and natively, in debug and release, and compare the runs; `--opt=`, `--engine=interp\|native`, `--run=<regexp>`, `--timeout=<seconds>`, `--jobs=<n>`, `--human` |
| `lsp` | A language server on standard input and output: diagnostics as you type, formatting, the compiler's fixes as quick fixes, and ownership facts on hover; setup for Neovim, Helix and Emacs is in [docs/editors.md](docs/editors.md) |

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
`std/str` and `std/map` code) and require the same agreement. Freestanding fixtures run under QEMU when it
is installed and are skipped otherwise; `KERN_REQUIRE_QEMU=1` (set in CI) turns
the skip into a failure. `golangci-lint run ./...` is part of CI, which runs on
Ubuntu and macOS (`.github/workflows/ci.yml`).

```bash
go run ./bench/harness
```

regenerates `bench/REPORT.md` from five runs per row with 95% confidence
intervals, checking that the C, Rust and kern implementations agree on their
checksums. In the latest report (2026-10-28) user-level kern code meets the
10% throughput target on eleven of the thirteen workloads, at 0.92× to 1.08× of
the best of C and Rust; thread_pool (0.88×) and lock_protected_map (0.76×) miss
it. Peak memory meets the 10% target on twelve of the thirteen: btree uses 1.23×
the memory of C, the width of its checked 8-byte handles (ADR-014).

## Status

The implementation went through five planned phases (`specs/initial/PLAN.md`,
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

## License

MIT. See [LICENSE](LICENSE).
