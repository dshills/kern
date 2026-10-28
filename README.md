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
candidate; the standard library covers the thirteen benchmark workloads and not
much more (no strings, maps, file or process I/O, or JSON yet; see
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
| Standard library | `std/core.kern` | Implicitly imported prelude: `Box`, `Vec`, `Pool`, `Arena`, `Mutex`, `Condvar`, `Atomic`, scoped threads, raw pointers. The containers are written in kern over audited `unsafe` blocks (`docs/unsafe-audit.md`) |
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

Check a file and read the diagnostics as text rather than JSON:

```bash
./kern check --human examples/btree/btree.kern
```

## Commands

Every command other than `run`, `build` and `debug` reads one JSON request on
standard input (or takes files on the command line) and writes one JSON response
on standard output; `--human` renders the response as text. The schema is in
`tools/agent-api/schema.md`.

| Command | What it does |
|---|---|
| `check` | Parse, resolve, type-check and ownership-check; diagnostics carry a code, a rule number and, where one exists, a machine-applicable fix |
| `format` | Canonical formatting (`spec/format.md`): idempotent and diagnostic-preserving |
| `emit-ir`, `emit-asm` | The semantic IR, or the LLVM IR the backend produces |
| `explain-ownership` | For a span or `--line=N`: each place's ownership state, drop point and accesses |
| `show-layout` | Exact sizes, alignments and field offsets for the target |
| `show-copies`, `show-allocations`, `show-bounds-checks`, `show-effects` | Where deep copies, heap allocations, runtime bounds checks and side effects occur, each labelled `proven`, `runtime-checked`, `assumed-unsafe`, `estimated` or `unknown` |
| `apply-structured-edit` | Apply a semantic edit from the machine API and return the new source |
| `run` | Build and execute natively; `--opt=debug\|release`, `--env=hosted\|freestanding`, `--stack-bytes=N`, `--test-hooks` |
| `build` | Produce an executable or freestanding image; `--debug` emits DWARF line tables |
| `debug` | Build with debug info and launch lldb with a `kern-own` command that shows ownership state at the current line |

## Testing and benchmarks

```bash
go test ./...
```

The fixture suite runs every program in both the interpreter and the native
backend and requires them to agree. Freestanding fixtures run under QEMU when it
is installed and are skipped otherwise; `KERN_REQUIRE_QEMU=1` (set in CI) turns
the skip into a failure. `golangci-lint run ./...` is part of CI, which runs on
Ubuntu and macOS (`.github/workflows/ci.yml`).

```bash
go run ./bench/harness
```

regenerates `bench/REPORT.md` from five runs per row with 95% confidence
intervals, checking that the C, Rust and kern implementations agree on their
checksums. In the latest report user-level kern code meets the 10% throughput
target on three of the thirteen workloads (ring_buffer, intrusive_run_queue and
page_frame_allocator), reaches 0.84× to 0.87× of the best of C and Rust on
vector, hash_table and buffer_pool, and 0.2× to 0.73× on the other seven; peak
memory meets the 10% target on eleven of the thirteen.

## Status

The implementation went through five planned phases (`specs/initial/PLAN.md`,
reports in `docs/reports/`): workload design, the core language and
interpreter, the native backend and ownership checker, concurrency and
freestanding targets, tooling and the machine API, and hardening (frozen
rules, DWARF, reproducible releases, security review). Open items are recorded
in the reports and ADRs rather than in an issue tracker.

Specification documents, the language reference and the ADRs are the
authority; where the code disagrees with them, the code is wrong.
