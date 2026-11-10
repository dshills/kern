# Runtime work: status

The ledger of `specs/runtime/SPEC.md` (gate 10). One row per requirement:
the commit that met it, and any deviation from the spec's text with its
reason. Updated at the end of every phase.

| Requirement | Phase | Commit | Deviations |
|---|---|---|---|
| RA1 source layout | 1 | c45f745 |  |
| RA2 warning-clean | 1 | c45f745 |  |
| RA3 ABI conformance | 1 | c45f745 | On Linux the second architecture is checked only when its system headers are installed (CI runs both natively) |
| RA4 ABI version | 1 | c45f745 |  |
| RA5 sanitized runs | 1, final | c45f745 (mode, test, fixes; complete at gate 8) | The list of known reports is empty: RC1's leak has no fixture yet |
| RA6 static analysis | 1 | c45f745 |  |
| RB1 configuration | 2 | c62a4d7 | The privileged case is tested through a test hook (`KERN_TEST_PRIVILEGED`) |
| RB2 standard output policy | 2 | c62a4d7 |  |
| RB3 robust writes | 2 | c62a4d7 | The recorded failure is standard output's; standard error's writes have no later call to report through. `EINTR` is retried but not provoked by a test |
| RB4 terminal query | 2 | c62a4d7 |  |
| RB5 exit | 2 | c62a4d7 |  |
| RC1 detached threads | 3 | 4dd62e2 |  |
| RC2 stack size | 3 | 4dd62e2 | `kern test` has no `--stack-bytes` (it honours `KERN_STACK_BYTES`); the manifest's error code is `PROJECT_INVALID`, as for every manifest error |
| RC3 spawn robustness | 3 | 4dd62e2 | A spawn whose allocation fails is `Err(SpawnErr)`; it trapped `oom` before (`specs/runtime-trap-file` R4) |
| RC4 queries | 3 | 4dd62e2 |  |
| RE1 line and column | 4 | e3f7ef2 | The span 0-0 (no location) also gives line 0, column 0 |
| RE2 backtrace | 4 | e3f7ef2 | A debug build lists every call as written only when made with `--backtrace` (clang inlines at `-O1`) |
| RE3 parity | 4 | e3f7ef2 |  |
| RD1 park | 5 | 5d37494 |  |
| RD2 timed wait | 5 | 5d37494 |  |
| RD3 try lock | 5 | 5d37494 | Built on two unsafe prelude operations, `Mutex.try_acquire` and `Mutex.held` |
| RD4 conditional assertions | 5 | 5d37494 | `Interior` takes no `where` clause |
| RD5 std/sync | 5 | 5d37494 | `Channel.capacity` added; one binding cannot hold two guards, so a nested read through one variable is a compile error, not a block |
| RF1 checked allocator | 6 | baf1818 | When the table cannot record a block that `realloc` already moved, unknown addresses stop being errors (no trap is added) |
| RF2 allocation report | 6 | baf1818 | A scope's table of joins is not counted; a thread became one allocation |
| RF3 page allocator | 6 | baf1818 | `alloc` traps `explicit` for a request that breaks an allocator's obligations |
| RG1 error kinds | 7 | PH7 | `io.error_of(kind)` added; `EACCES` has no test of a real failure (the container runs as root) |
| RG2 File | 7 | PH7 | The option is `trunc`: `truncate` is a reserved word. `io.options()` added |
| RG3 metadata and paths | 7 | PH7 | `copy_file` copies bytes, not the mode |
| RG4 readers and writers | 7 | PH7 | `write_to`, `read_to_end`, `with_capacity` and `BufWriter.buffered` added; `net.TcpStream` joins in phase 8 |
| RH1 addresses | 8 | | |
| RH2 TCP | 8 | | |
| RH3 UDP | 8 | | |
| RH4 rules | 8 | | |
| RI1 std/signal | 9 | | |
| RI2 std/rand | 9 | | |
| RJ1 board interface | 10 | | |
| RJ2 allocator checks | 10 | | |
| RK1 runtime reference | 11 | | |
| RK2 other documents | 11 | | |
| R-P1 performance | 11 | | |

## Found along the way

| Finding | Commit |
|---|---|
| Assignment through an access, to a field or to an element did not destroy the previous value (Rule 5.1.9) | 41b5593 |
| A temporary used as a method receiver was never destroyed (Rule 5.7.2) | 41b5593 |
| `memcpy` from a null pointer for empty sequences in the hosted runtime | c45f745 |
| Small integer arguments passed without the extension the C side assumes on x86-64 and Apple arm64 | c45f745 |
| The interpreter decremented a `Shared` count without the lock that guards its increment | 4dd62e2 |
| An owned `match` on a union refused payloads whose types have destructors; the rule refuses only a union that declares one itself | 5d37494 |
| The IR validator's worklist bound was too small for a long inlined function (`ownership state does not converge` on correct code) | PH7 |
| The interpreter's `print_str` crashed on a whole array argument | PH7 |
| String literals had no `\r` and no byte escape; a variant name shared by two unions could not be written bare; `match replace(...)` did not own its payload (language changes) | PH7 |
