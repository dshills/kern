# Runtime work: status

The ledger of `specs/runtime/SPEC.md` (gate 10). One row per requirement:
the commit that met it, and any deviation from the spec's text with its
reason. Updated at the end of every phase.

| Requirement | Phase | Commit | Deviations |
|---|---|---|---|
| RA1 source layout | 1 | c45f745 |  |
| RA2 warning-clean | 1 | c45f745 |  |
| RA3 ABI conformance | 1 | c45f745 | On Linux the second architecture is checked only when its system headers are installed (CI runs both natively) |
| RA4 ABI version | 1 | c45f745 | The number is 2 for the whole of this unreleased work: later phases changed entries that earlier phases had added (the address record, a datagram's sender) without a new number |
| RA5 sanitized runs | 1, final | c45f745 (mode, test, fixes; complete at gate 8) | The list of known reports is empty: RC1's leak has no fixture yet |
| RA6 static analysis | 1 | c45f745 |  |
| RB1 configuration | 2 | c62a4d7 | The privileged case is tested through a test hook (`KERN_TEST_PRIVILEGED`) |
| RB2 standard output policy | 2 | c62a4d7 |  |
| RB3 robust writes | 2 | c62a4d7 | The recorded failure is standard output's; standard error's writes have no later call to report through. `EINTR` is retried but not provoked by a test |
| RB4 terminal query | 2 | c62a4d7 |  |
| RB5 exit | 2 | c62a4d7 |  |
| RC1 detached threads | 3 | 4dd62e2 |  |
| RC2 stack size | 3 | 4dd62e2, b4f2b5b | `kern test` has no `--stack-bytes` (it honours `KERN_STACK_BYTES`); the manifest's error code is `PROJECT_INVALID`, as for every manifest error. A size below the system's minimum for a thread (131,072 bytes on Linux arm64) is raised to it: "the same on Linux and macOS" holds from that size up |
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
| RG1 error kinds | 7 | b764316 | `io.error_of(kind)` added; `EACCES` has no test of a real failure (the container runs as root) |
| RG2 File | 7 | b764316 | The option is `trunc`: `truncate` is a reserved word. `io.options()` added |
| RG3 metadata and paths | 7 | b764316 | `copy_file` copies bytes, not the mode |
| RG4 readers and writers | 7 | b764316 | `write_to`, `read_to_end`, `with_capacity` and `BufWriter.buffered` added; `net.TcpStream` joins in phase 8 |
| RH1 addresses | 8 | 407c1e7 | Addresses are taken by value (they copy), not by `view`; `parse_ip`, `format_ip` and the constructors (`v4`, `addr`, `loopback_v4`, ...) added. An IPv6 address has no scope identifier |
| RH2 TCP | 8 | 407c1e7 | `TcpListener.bind_with_backlog` added |
| RH3 UDP | 8 | 407c1e7 | `recv_from` returns `Datagram { len, from }`: kern has no tuple type |
| RH4 rules | 8 | 407c1e7 | A signal interrupting a blocked call is retried in the code but not provoked by a test until phase 9. A connect to a full queue is turned away on macOS and left waiting on Linux; the test accepts both |
| RI1 std/signal | 9 | 6296fee | In the interpreter a signal at its default does what the Go runtime does (User1, User2 and Pipe do nothing), a Pipe sent from outside is not reported, and `reset` cannot undo an ignore inherited at start |
| RI2 std/rand | 9 | 6296fee | `Rng.from_state` and `Rng.state` added |
| RJ1 board interface | 10 | a176db2 | The heap is at most 1,073,741,824 bytes. The interpreter's emulation has no arena limit and no board. The x86-64 clock is calibrated at its first reading, not in `kern_board_init` |
| RJ2 allocator checks | 10 | a176db2 |  |
| RK1 runtime reference | 11 | b4f2b5b |  |
| RK2 other documents | 11 | b4f2b5b | The unsafe audit and the security review are extended by hand; neither is generated |
| R-P1 performance | 11, final | b4f2b5b; measured again at 6f4dc1f (twelve of thirteen at or below v0.1.0) | At phase 11, ten workloads at or below v0.1.0. `hash_table` (+2.1%) and `lock_protected_map` (about +8% over fifteen runs) free tables that v0.1.0 leaked (rule 5.1.9, phase 1); `thread_pool` is within its run-to-run spread. `kern_mutex_lock`'s trap tail is shaped differently; the uncontended path and `unlock` are the same instructions |
| RL1 Unix-domain sockets | 12 | 04bdaed | `UnixPair` is a one-variant union (`Ends(a, b)`), which a `match` takes apart: kern cannot move two fields out of a struct. `connect_timeout` added |
| RL2 directories | 12 | 04bdaed | `io.join` added |
| RL3 file locks | 12 | 04bdaed | `unlock` returns `Result[(), IoErr]` |
| RL4 mapped files | 12 | 04bdaed | The interpreter's mapping was a copy until phase 13 (RM5); it is the system's mapping now |
| RL5 freestanding stack and start-up | 12 | 04bdaed |  |
| RL6 the Linux gate boots images | 12 | 04bdaed | The container requires QEMU (`KERN_REQUIRE_QEMU=1`): a missing emulator fails the gate |
| RM1 locks on ranges | 13 | 813cc0f |  |
| RM2 IPv6 scopes | 13 | 813cc0f | `to_wire`/`from_wire` and every record are 23 bytes. An all-digit zone is always an index. The Linux gate's container runs on a network with IPv6 so that the test on a real link runs there |
| RM3 Unix-domain datagrams | 13 | 813cc0f |  |
| RM4 socket options | 13 | 813cc0f | A hop limit outside 1 to 255 is refused before the system is asked |
| RM5 the interpreter maps files | 13 | 813cc0f | The interpreter's record of a fault has the span 0-0, where the runtime's names the function when its walk finds one |
| RM6 the interpreter's signal defaults | 13 | 813cc0f | The interpreter gives a signal the system's default with a raw `sigaction`, and exits with 128 + the signal's number should that fail |
| RN1 readiness | 14 | 6f4dc1f | `Ready`'s fields are `closed` and `failed`. A wait reports at most 64 sockets, the ready ones in turn (`net_poller_many.kern`, added with the final gates) |
| RN2 descriptors over a Unix-domain stream | 14 | 6f4dc1f | `Received` is a union, so that a `match` takes the descriptor out. `io.file_of(fd)` added for the library's use. macOS does not answer `SO_ACCEPTCONN`: a listener is told from a stream there by the process's own descriptor table (`proc_pidfdinfo`) |
| RN3 peer credentials | 14 | 6f4dc1f |  |
| RN4 abstract names | 14 | 6f4dc1f | In the interpreter on Linux a file's relative path that begins with `@` is bound as `./@...`: its host's network package reads a leading `@` as an abstract name |

## Found along the way

| Finding | Commit |
|---|---|
| Assignment through an access, to a field or to an element did not destroy the previous value (Rule 5.1.9) | 41b5593 |
| A temporary used as a method receiver was never destroyed (Rule 5.7.2) | 41b5593 |
| `memcpy` from a null pointer for empty sequences in the hosted runtime | c45f745 |
| Small integer arguments passed without the extension the C side assumes on x86-64 and Apple arm64 | c45f745 |
| The interpreter decremented a `Shared` count without the lock that guards its increment | 4dd62e2 |
| An owned `match` on a union refused payloads whose types have destructors; the rule refuses only a union that declares one itself | 5d37494 |
| The IR validator's worklist bound was too small for a long inlined function (`ownership state does not converge` on correct code) | b764316 |
| The interpreter's `print_str` crashed on a whole array argument | b764316 |
| String literals had no `\r` and no byte escape; a variant name shared by two unions could not be written bare; `match replace(...)` did not own its payload (language changes) | b764316 |
| A match on the value `?` unwraps from a call did not own its payload (language change) | 407c1e7 |
| No way to exchange two move-only elements of a sequence: the prelude gained `swap_at` (language change) | 6296fee |
| The entry thunk's `noinline` kept LLVM from optimizing `main` a second time (`ring_buffer` +20%, `wal` +12%): the thunk carries `main`'s marker and is inlined again | b4f2b5b |
| Every program imported the whole runtime's system functions (2.4 million instructions at launch on macOS): the link drops unreached runtime code | b4f2b5b |
| `tests/run/std_io.kern` wrote to the working directory, which the Linux container mounts read-only | b4f2b5b |
| A configured stack of 65,536 bytes could not start `main` or a thread on Linux arm64, whose minimum is 131,072 (found by the final run of the whole suite in the container) | b4f2b5b |
| Go's network package permits broadcast on every datagram socket and the system on none: the interpreter's UDP sockets could broadcast without `set_broadcast` | 813cc0f |
| A signal that `signal.raise` sent with a fatal disposition ended a native program before its buffered output was written; `raise` flushes first | 813cc0f |
| A `?` in an argument of a call whose earlier argument had an access open on a value with a destructor was an internal error of the compiler (`access conflicts with open access`): the error path now ends the accesses that enclosing calls opened | 6f4dc1f |
| On the error path of a `?` in an operand of a call, a struct literal, a variant or an array, the owned operands already evaluated were never destroyed (Rule 5.7.2): they are destroyed there, newest first | 6f4dc1f |
| On macOS a descriptor sent over a Unix-domain socket and not taken (read past by a plain read, sent with a datagram, or beyond the first of a message) stayed open in the receiving process, in both engines, against RN2: every such receive gives room for all a message can carry and closes the rest (found after the first run on GitHub's macOS host) | 8e34f9b |
