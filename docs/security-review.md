# Security review (PLAN.md Phase 5)

**Date:** 2026-10-26. **Scope:** the unsafe boundaries of the language, the hosted and
freestanding runtimes, and the machine API's handling of untrusted input (JSON requests,
file paths, structured edits). Each finding is closed by a change in this phase or accepted
in writing with its rationale.

## 1. Unsafe boundaries

| Item | Finding | Status |
|---|---|---|
| Raw pointers (rule 6.6) | every `ptr_*` operation and `SystemAllocator.alloc`/`free` require `unsafe` and a contract on the enclosing function; the interpreter traps on unmapped addresses, natively a bad address is undefined behaviour inside `unsafe` | accepted: the §6 contract; `docs/unsafe-audit.md` lists every block |
| `unsafe assert Send/Share/Interior` | an incorrect assertion makes a data race expressible (`tests/concurrency/race_raw_ptr.kern` is the deliberate case) | accepted: ThreadSanitizer builds (`tsan: clean`) run every concurrency fixture; the audit records each assertion's obligation |
| Trap handlers (ADR-012) | a non-conforming handler may deadlock or trap twice; never a memory-safety issue | accepted: documented obligation, nested-trap path terminates (`tests/native_test.go`) |
| Packed field access, inline assembly, Interior escalation (rule 6.7) | all unsafe, contract-gated | closed: `UNSAFE_OP_OUTSIDE_UNSAFE` fixtures |
| Unsafe operations as function values | taking `ptr_load` as a value would bypass the `unsafe` requirement at the call | closed in Phase 4 (`UNSAFE_OP_OUTSIDE_UNSAFE` at the reference) |

## 2. Runtime (`runtime/hosted/src/kern_rt.c`, `runtime/freestanding/src/rt.c`)

| Item | Finding | Status |
|---|---|---|
| Size arithmetic | vector growth and allocation sizes overflow checked against `SIZE_MAX`/`INT64_MAX`; pool growth is transactional | closed (Phase 2 review) |
| Trap path | `write(2)`-only default handler on preallocated per-thread buffers; file names JSON-escaped (hosted and freestanding); nested traps abort | closed |
| Mutex | error-checking pthread mutex; recursive acquisition traps instead of deadlocking | closed |
| Reference counts and atomics | compare-exchange loops; narrow read-modify-writes are single atomic updates | closed |
| Test hooks | linked only with `--test-hooks` (`-DKERN_TEST_HOOKS`); release builds cannot reference them | closed |
| Console device (freestanding) | polls the UART status before each byte; no buffering | closed |
| Detached threads | the result block of an unjoined thread leaks | accepted: documented limitation (no safety impact) |

## 3. Machine API input handling (`cmd/kern`)

| Item | Finding | Status |
|---|---|---|
| JSON requests | one JSON document per request, decoded with the standard decoder; a second document is `API_MALFORMED_REQUEST`; unknown fields are ignored (lenient per §16, which lists the fields); malformed JSON exits 2, never panics | closed: `FuzzRequest` (`cmd/kern/fuzz_test.go`) feeds arbitrary JSON and bytes and requires exit 0, 2 or 3 without a panic |
| File paths | `files` are opened as given (relative to the working directory, absolute allowed); the API is a local developer tool run with the user's own permissions, and the structured edit never writes to disk (it returns the new sources) | accepted: no sandbox; documented here and in `tools/agent-api/schema.md` |
| Spans | `explain-ownership` validates the span's file, bounds and UTF-8 boundaries (`API_INVALID_SPAN`); structured-edit coordinates are validated per op (`EDIT_INVALID_SPAN`) before anything is applied | closed |
| Request size | no limit on request or file size; a very large file costs memory and time proportionally | accepted: local tool |
| `emit-asm`, `build` | invoke `clang` from the PATH with the emitted IR and the embedded runtime; no user-controlled flags reach clang except the target triple (`--target`), which is passed as one argument | accepted: the triple is validated by clang itself |
| Incremental cache | entries are JSON files named by their key hash under the directory the user names; corrupt or mismatched entries are ignored (recomputed), temporary files are unique per writer | closed |
| Trap records | file names from the request are escaped in JSON output | closed |
| Interpreter resources | `--stack-bytes` bounds the interpreter's stack; the raw heap rejects requests above `1 << 40` bytes | closed |

## 4. Residual risks

- Native programs built from `unsafe` code are as safe as that code; the audit, not the compiler, is the guarantee.
- The compiler trusts the machine API caller: it is not a service boundary.
- No cryptographic signing of release artifacts; `scripts/release.sh` produces checksums and reproducible binaries so a build can be verified by rebuilding.
