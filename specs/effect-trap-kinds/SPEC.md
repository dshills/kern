# Trap kinds in `show-effects`, and a fallible `Map` insert

**Version:** 0.1 (2026-11-01). **Scope:** the effect analysis
(`compiler/analysis/effects.go`), the §16 result type and its schema
(`compiler/diag/schema.go`, `compiler/diag/diag.go`,
`tools/agent-api/schema.json`, the generated `tools/agent-api/schema.md`),
`std/map` (`compiler/resolve/prelude/map.kern`, mirrored in
`std/map.kern`), tests, and the documents that describe effects and wire
versions. **Language impact:** none to the grammar or the checker; the
wire format gains schema version 0.4.
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §6 (trap
kinds), §16 (`show-effects`); `specs/diag-message/SPEC.md` (how 0.3 was
added while 0.2 kept its shape); `specs/fallible-alloc/SPEC.md` and
`specs/alloc-containers/SPEC.md` (the forms that must show no `oom`;
`docs/reports/fallible-alloc.md`'s first open question).

**Review status.** speccritic (gpt-6.1-sol, `specs/diag-message` as
context), three rounds: one critical finding in the first (byte-identical
0.3 output contradicted the corrected `traps` of R3; resolved: the
correction applies at every version), and the warnings (algorithm in
normative text, the omitted-version case, the sort order) folded in.

**Glossary.** R*n*: requirement *n* of §4. Trap kind: one of spec §6's
thirteen kinds. Listed kinds: the twelve kinds other than `stack`.
Effect summary: the per-function result of `show-effects`. Runtime object
operations: `Shared.new`, `Mutex.new`, `Condvar.new`, `thread.scope`,
`thread.spawn`, `scope.spawn`, whose storage the C runtime allocates
through `kern_alloc`, which traps `oom` (and the interpreter's
`runtimeAlloc`, which does the same). Abbreviations: IR, intermediate
representation; FFI, foreign function interface; LLVM, the native
backend's target; LSP, the language server.

## 0. Goals

A program, an agent or a reviewer can ask the compiler whether a
function can trap `oom` (or any other kind) and get an answer that a
`try_` form passes: today `show-effects` reports one `traps` flag, which
the fallible forms set because the allocator checks its §6 obligations
with an `explicit` trap. `std/map` also gains the fallible insert it
lacks, so the standard containers of a kernel-style program can all be
used without an `oom` path.

Non-goals: per-call-site trap kinds, proving a kind impossible beyond
what the IR shows, fallible forms of the runtime objects, a `Map`
`try_reserve`.

## 1. Background

`effectSummaries` computes `allocates`, `traps`, `spawns` and `unsafe`
transitively over statically resolved callees and destructors; a call
through a function value or into FFI sets all four with label `unknown`
or `assumed-unsafe`. `traps` is set by `CheckBounds`, `CheckOverflow`,
`CheckConv`, `Trap`, an `Unreachable` terminator, and the intrinsics in
the `trapping` table. The runtime object operations trap `oom` natively
and in the interpreter but are not in that table, so a function that only
creates a `Mutex` is reported `traps: false`, which is wrong.

`Map.insert` rebuilds the table when it fills (`rebuild`, which builds a
new map with `with_bits` and moves every live entry into it with
`insert`); `with_bits` allocates through `Vec.resize` and `Vec.push`,
which trap `oom`.

The result schema has `additionalProperties: false`, so a new field is a
breaking change for a strict 0.3 consumer; schema 0.3 was introduced the
same way (`specs/diag-message`): a new version, with older requests
answered in their own shape.

## 2. Design

**Wire version 0.4.** `diag.SchemaVersion` becomes `"0.4"`; `Accepted` is
0.2, 0.3 and 0.4. A request at 0.4, and every command given files on the
command line, is answered at 0.4. A request at 0.3 is answered exactly
in the 0.3 shape, and at 0.2 in the 0.2 shape (no diagnostic
`message`). The only difference between the 0.3 and 0.4 shapes is
`trap_kinds`. The correction of R3 (the runtime object operations trap
`oom`) is a fix to the analysis, not a wire change: it applies at every
version, so a 0.3 or 0.2 response reports `traps: true` for a function
that creates a `Mutex`, where it wrongly reported `false` before.

A request with no `schema_version` or an unsupported one, and a request
that is not valid JSON, is answered as today at the newest version, now
0.4 (`API_UNSUPPORTED_SCHEMA`, `API_MALFORMED_REQUEST`).

**`trap_kinds`.** Each `show-effects` function entry at 0.4 gains
`trap_kinds`: the listed kinds the function may raise, in the order of
spec §6's enumeration (`bounds`, `overflow`, `div_zero`, `shift`,
`conversion`, `generation`, `vacant`, `identity`, `disjoint`, `oom`,
`explicit`, `unreachable`), no duplicates, transitive like the other effects. `stack` is never listed:
every function can raise it (a stack check in each prologue), which the
schema text says. `traps` is unchanged in meaning and is true iff
`trap_kinds` is non-empty. A kind is listed when the function or a
statically resolved callee or destructor contains:

| Source | Kinds |
|---|---|
| `CheckBounds` | `bounds` |
| `CheckOverflow` with `+`, `-`, `*` | `overflow` |
| `CheckOverflow` with `/`, `%` | `div_zero`, and `overflow` for a signed type |
| `CheckOverflow` with `<<` | `shift`, `overflow` |
| `CheckOverflow` with `>>` | `shift` |
| `CheckConv` | `conversion` |
| `Trap` | its kind |
| an `Unreachable` terminator, unless a `Trap` precedes it in its block (the terminator after a trap is dead) | `unreachable` |
| `Mutex.lock`, `Condvar.wait`, a release of a `Mutex` lock | `explicit` |
| `Shared.clone` | `overflow` |
| `SystemAllocator.alloc`, `realloc`, `alloc_zeroed`, `free`; the `ptr_*` intrinsics in the table; `testing.require_hooks`, `testing.seed_shared_count` | `explicit` |
| the runtime object operations | `oom` (new: they also set `traps`) |
| `asm`, a call through a function value, an FFI call | every listed kind |

The table is the analysis's (`trapping` becomes a map from intrinsic to
kinds); a lowering or runtime change that adds a trap updates it, and a
test pins it against the runtime's trap sites (R5).

**`Map` fallible forms.**

```kern
impl Map[K, V] {
    func try_new() -> Result[Map[K, V], AllocErr]
    func try_insert(edit self, key: own K, val: own V) -> Result[Option[V], AllocErr] where K: Hashable
}
func try_with_bits[K, V](bits: u32) -> Result[Map[K, V], AllocErr]
```

- `try_with_bits(bits)` builds the same empty map as `with_bits(bits)`
  (same range check) or returns `Err(OutOfMemory)` when an allocation
  fails, leaking nothing. `try_new` is `try_with_bits(4)`.
- `try_insert` succeeds exactly when `insert` would not need to grow, or
  when growing succeeds; on success the map's entries, `len()` and the
  returned replaced value are those `insert` gives for the same
  sequence. When growing fails it destroys `key` and then `val` and
  returns `Err(OutOfMemory)` with the map's entries and `len()`
  unchanged. Once the larger table exists, moving the live entries into
  it makes no allocation call, so no `oom` trap is reachable from
  `try_insert` through library code (R4).
- `insert`, `rebuild` and `with_bits` are not changed.

*Implementation guidance (non-normative):* `try_with_bits` uses
`Vec.try_resize` for the states and `Vec.try_reserve` of the table size
for the key and value vectors, after which filling them with `None` does
not allocate. The new table always has room for the move (a same-size
rebuild happens only when `count + 1 <= cap / 2`; a doubled table holds
fewer than 3/8 of its slots), so the move uses a placing step that skips
`insert`'s growth check.

## 3. Out of scope

- Trap kinds per call site, or a kind reported impossible because a
  guard dominates a check (the proof pass already removes proven
  checks; what remains is listed).
- Fallible runtime object operations (`kern_alloc` traps; a C ABI
  change).
- `Map.try_reserve`, an allocator-generic `Map`.
- `show-effects` output in the LSP (it shows none today).

## 4. Requirements

**R1. Version 0.4.** `SchemaVersion` is 0.4 and 0.2, 0.3 and 0.4 are
accepted. A 0.3 request's `show-effects` response has no `trap_kinds`
and otherwise the 0.3 shape, with the `traps` values of the corrected
analysis (R3); for a program none of whose functions calls a runtime
object operation it is byte-identical to today's. A 0.4 response has
`trap_kinds` on every function. *Verified by:* `compiler/diag` tests (the 0.3 shape has
no `trap_kinds`, 0.4 has it); `cmd/kern` tests that run `show-effects` at
each version, and the existing version tests updated to 0.4 where they
name the newest version.

**R2. `trap_kinds` contents.** For each source row of §2 a test program
whose function contains only that source reports exactly that row's
kinds; a caller reports the union of its callees'; `traps` is true iff
the list is non-empty. *Verified by:* `compiler/analysis` tests, one
case per row.

**R3. Runtime object operations trap `oom`.** A function that calls
`Mutex.new` (and each other runtime object operation) reports
`traps: true` and `oom`. *Verified by:* the R2 cases.

**R4. Fallible forms list no `oom`.** At `T = u64`, every `try_` method
of `Box`, `Vec` and `String`, every `BoxIn` and `VecIn` method (`A =
SystemAllocator`) and `Map.try_new` / `try_insert` (`K = V = u64`) has an
effect summary without `oom`; `Box.new`, `Vec.push` and `Map.insert`
have one with it. The exception is `Box.try_new_with` and
`try_new_with_ctx`: they call the caller's initializer through a
function value, so their summaries list every kind with label `unknown`
(element code, as `specs/fallible-alloc` R4 excludes it); their own code
reaching no `oom` trap is `TestTryFormsReachNoOOMTrap`'s. *Verified by:* an analysis test over one program
calling them all.

**R5. The table matches the runtime.** Every runtime entry that raises a
trap (`kern_trap_loc` / `kern_trap` call sites in
`runtime/hosted/src/kern_rt.c`, and the interpreter's `runtimeAlloc` /
`it.trap` sites for intrinsics) corresponds to an intrinsic whose table
entry lists that kind. *Verified by:* a test that lists the runtime's
trapping entry points with their kinds (a fixed table in the test,
checked against the sources by name) and compares it with the analysis
table.

**R6. `Map` fallible forms.** *Verified by:* `tests/run/map_try.kern`
(all four configurations: `try_insert` gives the same entries, `len()`
and replaced values as `insert` over a sequence that rebuilds at the
same size and doubles; `try_new` succeeds) and `tests/run/map_try_oom.kern`
(test hooks: `testing.fail_next_alloc()` before `try_new`, and before a
`try_insert` that must rebuild, yields `Err`; the map's entries and
`len()` are unchanged, and a key and a value whose destructors print are
destroyed once each, key first); R4's walk.

**R7. Existing programs unchanged.** The LLVM IR of the
`scripts/capture-ll.sh` corpus is byte-identical (the analysis does not
affect code generation; the `Map` additions are not instantiated by
existing programs). *Verified by:* a baseline capture and `cmp`.

**R8. Documents.** `tools/agent-api/schema.json` (0.4, `trap_kinds`,
the version note), `schema.md` regenerated (`go generate ./compiler/diag`),
the README's version paragraph, `docs/ownership.md` rule 5.6.4's
effect sentence, `docs/reports/fallible-alloc.md`'s open question marked
resolved, and `docs/reports/effect-trap-kinds.md`. *Verified by:* the
schema staleness test; review.

## 5. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` clean;
R7 byte-identical; documents of R8 updated.

## 6. Risks

- **Consumers pinned to 0.3.** The agent harness sends 0.2 requests and
  is unaffected; a consumer must name a version, so none receives 0.4
  without asking, except through files on the command line (as 0.3 was
  introduced).
- **Over-reporting.** The table is conservative (a `ptr_*` intrinsic
  lists `explicit` because the interpreter checks raw memory); a kind
  listed is possible, not certain, as `traps` always meant.
