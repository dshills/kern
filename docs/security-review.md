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
| Mutex | the runtime's own parking lock (a 32-bit state threads park on, the holder's id beside it, written only by the holder); recursive acquisition traps `explicit` before any wait instead of deadlocking; a wait without the lock or with a second Mutex on one Condvar traps | closed (`docs/reports/perf-gaps.md`) |
| Reference counts and atomics | compare-exchange loops; narrow read-modify-writes are single atomic updates | closed |
| Test hooks | linked only with `--test-hooks` (`-DKERN_TEST_HOOKS`); release builds cannot reference them | closed |
| Console device (freestanding) | polls the UART status before each byte; no buffering | closed |
| Detached threads | the result block of an unjoined thread leaked | closed 2026-11-09: the thread destroys its result and frees its record when it ends (section 6) |

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
- No cryptographic signing of release artifacts; `scripts/release.sh` produces checksums and reproducible binaries so a build can be verified by rebuilding. Each release publishes `SHA256SUMS` beside its archives (section 5).

## 5. Release pipeline (added 2026-11-08, v0.1.0)

`.github/workflows/release.yml` publishes a release when a `v*` tag is pushed (`docs/releasing.md`, `specs/release-v0.1/SPEC.md`).

| Item | Finding | Status |
|---|---|---|
| Token scope | the workflow's `GITHUB_TOKEN` has `contents: write` (create the release, upload assets) and `actions: read` (find the `ci` run); no other permission and no stored secret | accepted: the minimum the workflow needs |
| What gets released | the tagged commit must be an ancestor of `main` and have a successful `ci` run on a push; a tag pushed on any other commit stops the workflow before anything is built | closed |
| Build integrity | the workflow rebuilds every target twice and fails on any difference before building the archives; the archives are byte-identical across runs (`gzip -n`, sorted GNU tar entries, fixed owner and time), so anyone can rebuild a release at its tag and compare against `SHA256SUMS` | closed |
| Partial publication | the release is created as a draft with every asset and made public only after the uploads succeed; recovery from a failure in between is a fixed table in `docs/releasing.md`, and assets are never uploaded by hand | closed |
| Third-party actions | `actions/checkout@v4` and `actions/setup-go@v5`, referenced by major-version tag rather than commit hash, as in `ci.yml` | accepted: pinning by hash is open |
| Signing | as in section 4: checksums, not signatures | accepted |

## 6. The runtime work (added 2026-11-09, `specs/runtime`)

The runtime grew from the language's own needs to files, sockets, signals
and child-facing descriptors. What that adds to the review:

| Item | Finding | Status |
|---|---|---|
| Environment variables | `KERN_BACKTRACE`, `KERN_STACK_BYTES` and `KERN_ALLOC_REPORT` are read once at startup, accept only their listed forms, and are ignored by a set-user-ID or set-group-ID process; none names a path or is passed to another program | closed (`TestRuntimeConfigurationVariables`) |
| Descriptors | every file, socket and pipe the runtime opens is close-on-exec and numbered above 2; a child gets the descriptors `std/process` names and no other; a standard descriptor closed at startup is filled before anything is opened | closed (`TestProcInheritFile`, `net_timeouts.kern`'s descriptor counts) |
| Paths | a path with a NUL byte or of 4,096 bytes or more is refused before any system call; `create_new` fails on an existing path, a symbolic link included (`O_EXCL`); the library does not follow or resolve paths on its own account | closed (`io_file.kern`, `io_paths.kern`) |
| Temporary files | made with `mkdtemp` and `mkostemp` under `TMPDIR` | closed (unchanged) |
| Sockets | no call can block past its duration; a write to a closed peer is an error, never a signal; an IPv6 socket is IPv6 only; nothing listens unless the program binds | closed (`net_timeouts.kern`) |
| Name resolution | `net.resolve` hands the name to the system resolver after refusing an empty name, one of 256 bytes or more and one with a NUL byte | accepted: the resolver's behaviour is the system's |
| Signals | the handler sets one bit and writes one byte to a non-blocking pipe: it takes no lock and allocates nothing; a child never inherits a disposition the program set | closed (`signal_children.kern`, `signal_interrupts.kern`) |
| Random numbers | `rand.fill`, `u64` and `below` are the system's generator (`getentropy`); `Rng` is documented as unfit for secrets | closed (`rand_os.kern`); the distinction is the reference's first paragraph |
| Allocator checks | a debug build finds a double free, a wrong size and a foreign pointer at the call; a release build does not check, as before | closed for debug builds (`tests/trap/sysalloc_*.kern`); accepted for release |
| Trap record and backtrace | written with `write(2)` from a fixed buffer; the walk reads the stack through a pipe, so a corrupted frame chain ends the walk instead of faulting; file and function names are JSON-escaped | closed (`TestBacktrace`) |
| Detached threads | the result of an unjoined thread is destroyed and its record freed when the thread ends | closed: the limitation section 2 accepted is gone (`thread_detached_result.kern`) |
| Board files | a board file is C the user supplies and is compiled into the image as given | accepted: it is the user's own code, as `extern` C is |
| File locks | whole-file and range locks are advisory: they bind only programs that ask, and the reference says so; a lock is the open file's and ends with it, so a program that dies holds none | accepted: advisory locking is the system's model (`io_lock.kern`, `io_lock_range.kern`) |
| Mapped files | a mapping is shared with every program that has the file open; a file cut short under a mapping faults, which is a trap of kind `unreachable` that names no secret; a mapping made for reading cannot be written, in safe code (`bytes_mut` traps) or in `unsafe` (the system faults) | closed (`TestMappedFileFaultsAreTraps`, `mem_map_readonly_mut.kern`) |
| Unix-domain sockets | a path is refused before any system call when empty, of 104 bytes or more, or holding a NUL byte; access is the file system's permissions on the path; a datagram's sender path is what the system reports and is cut at 104 bytes | closed (`net_unix.kern`, `net_unix_dgram.kern`) |
| Socket options and scopes | options need no privilege and are the socket's own; a scope is an interface index the system checks; an interface name of 16 bytes or more is refused before the lookup | closed (`net_options.kern`, `net_scopes.kern`) |
| Descriptors from another process | a received descriptor is close-on-exec from the moment it exists (atomically on Linux) and is closed when its `Descriptor` is destroyed; every descriptor after the first in a message, and one that an ordinary read passes over, is closed at once, so a peer cannot fill the program's descriptor table; the program names the type it expects and the runtime checks it against the system's answer before the descriptor is used; a file received shares its offset and its access mode with the sender's, which the reference states | closed (`net_descriptors.kern`, `TestDescriptorsBetweenProcesses`) |
| Peer credentials | read from the system (`SO_PEERCRED`; `getpeereid` and `LOCAL_PEERPID`), never from the peer: they are what the peer was when it connected | closed (`TestDescriptorsBetweenProcesses`) |
| Readiness queue | a `Poller`'s descriptor is close-on-exec; tokens are the program's own numbers and never dereferenced; a closed socket leaves the queue with the close | closed (`net_poller.kern`, `net_poller_threads.kern`) |
| Abstract socket names | a name in Linux's abstract namespace has no file permissions: any process in the network namespace may connect, as the reference says of the namespace; `peer_credentials` is the check to make | accepted: the namespace's own model (`TestAbstractSocketNames`) |
| The interpreter and signals | under `kern run` the interpreter restores the system's default for a signal with one `sigaction` call and sends the signal to itself; a program run inside another process (a test) never changes that process's signals | closed (`TestSignalDefaultsEndTheProgram`, `TestSignalDefaultFromOutside`) |
| Sanitized corpus | every runnable fixture passes under AddressSanitizer and UndefinedBehaviorSanitizer on macOS and Linux with an empty list of known reports | closed (`TestFixturesSanitized`); it found three defects in the compiler's destruction of values, all fixed |

