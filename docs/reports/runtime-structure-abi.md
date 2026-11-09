# Runtime, phase 1: structure, ABI checks, sanitized runs

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area A (RA1 to
RA6). **Plan:** `specs/runtime/PLAN.md`, phase 1. **Hosts:** macOS 27.2 on
arm64 (clang 23.1.4), and Ubuntu 24.04 on arm64 in the `kern-linux-check`
container (clang 18, glibc 2.39).

This is the first of eleven phases that complete the runtime. It adds no
facility a program can call. It makes the runtime's interface explicit and
checks it against the compiler, and it turns two sanitizers on the fixture
corpus. The sanitizers found five defects, two of them in the compiler.

## What changed

- **Layout (RA1).** `runtime/hosted/src/kern_rt.c` was one file of 1,443
  lines. It is now a list of includes: `kern_rt.h`, then `rt_base.c`
  (standard output, exit), `rt_io.c`, `rt_proc.c`, `rt_trap.c`,
  `rt_alloc.c`, `rt_time.c`, `rt_str.c`, `rt_sync.c`, `rt_thread.c` and
  `rt_start.c`. The move changed no logic.
  - `kern_rt.h` declares each of the 81 entries with its C prototype and a
    one-line contract. `kern_rt_fs.h` does the same for the freestanding
    runtime's 28.
  - `runtime/hosted` embeds the directory and builds `hosted.Source`, the
    whole runtime as one text with `#line` directives naming the units. A
    build compiles that text, so the runtime-object cache, keyed on it,
    follows an edit to any unit or to the header.
- **Startup contract (RA4).** A program's entry now calls the setters, then
  `kern_start(ptr @kern_rt_abi_2)`, then `kern_run_main(ptr @kern.entry)`,
  whose result is the exit status. `kern.entry` is a thunk that calls the
  program's `main` and widens its result; it carries `main`'s location
  marker. Both runtimes define `kern_rt_abi_2`. In a freestanding image the
  startup assembly no longer calls `kern_start` itself; the image's entry
  does.
- **Warnings (RA2).** Both runtimes compile clean under `-std=c11 -Wall
  -Wextra -Wshadow -Wmissing-prototypes -Wstrict-prototypes -Wvla -Werror`.
  The hosted runtime needed one change (the trap paths are now
  `_Noreturn`); the freestanding one needed its prototypes and one variable
  moved under the condition that uses it.
- **Sanitized builds (RA5).** `native.Options.Sanitize` builds the program
  and the runtime with AddressSanitizer and UndefinedBehaviorSanitizer, and
  the emitter marks every function `sanitize_address`.

## ABI conformance (RA3)

`TestRuntimeABI` reads each runtime's definitions from clang's own LLVM IR
and compares them with every declaration of a runtime entry the emitter
writes while lowering the corpus (287 programs: the hosted-only inventory's
corpus plus the concurrency, FFI and freestanding fixtures).

- **It found a mismatch on the first run.** The emitter passed `u8` and
  `bool` arguments (`print_bool`, `kern_io_open`, `kern_io_temp`,
  `kern_proc_wait` and others) as plain `i8`. On x86-64 and on Apple's arm64
  convention the C side declares them `zeroext`: the caller extends, and the
  callee may read the whole register. The values happened to be clean
  because each came from a `zext`, so nothing misbehaved; nothing
  guaranteed it either.
- **The rule depends on the target.** The standard AArch64 convention
  (Linux, bare metal) leaves the extension to the callee, and clang writes
  no attribute there. `smallIntExt(triple)` in the emitter encodes this:
  `zeroext` everywhere except AArch64 outside Apple's platforms. The test
  checks it on four targets from macOS (arm64 and x86-64 macOS, both
  bare-metal triples) and on Linux arm64 in the container; CI's two Linux
  runners cover Linux on both architectures.
- **Two entries had no fixture.** `kern_proc_pid` (now reached by
  `tests/run/process_child_id.kern`) and `eprint_str` (reached by an inline
  program in the test, since the harness reserves standard error for the
  trap record). The list of unreferenced entries is empty.

## What the sanitizers found (RA5)

`TestFixturesSanitized` runs the 121 `tests/run` and `tests/concurrency`
fixtures that end normally or trap through a check. On macOS it passed at
once (that platform's AddressSanitizer has no leak checker, and its C
library does not mark `memcpy`'s arguments non-null). On Linux, 17 fixtures
failed, for five reasons:

1. **`memcpy` from a null pointer** in `kern_out` (`print_str` of an empty
   sequence) and in `kern_cstr` (an empty directory or search path). A
   kern sequence of length 0 may carry a null pointer; `memcpy` must not be
   given one even with length 0. Fixed with `kern_copy`, used for every
   copy whose bytes come from compiled code (16 call sites).
2. **Assignment did not destroy the previous value** (compiler). Rule 5.1.9
   says an assignment evaluates the new value, destroys the old one, then
   stores. Only an assignment to a whole owned variable did. `self.f = v`,
   `self = v`, an assignment through an `edit` parameter or a `with`
   binding, and an assignment to a field or an array element all leaked
   the value they replaced, in both engines alike, which is why no
   differential test saw it. `std/map`'s rebuild (`self = bigger`) leaked
   the old table on every growth.
   - The checker now marks such assignments (`Info.AssignDrop`), and
     lowering emits the drop before the store.
   - The first store into storage that holds no value (an uninitialized
     variable, a field of `out` storage) still destroys nothing; a second
     store into the same `out` field does.
   - `tests/run/assign_destroys_previous.kern` covers ten forms.
3. **A temporary used as a method receiver was never destroyed**
   (compiler). `join(dir, name).as_view()` built a `Vec`, viewed it, and
   left it. Lowering now destroys such a temporary when the call returns,
   or, when the call returns an access (which may be derived from it), when
   that access ends. `tests/run/temp_receiver_drop.kern`.
4. **A fixture wrote into a read-only directory.** The sanitized run now
   gives each program a temporary working directory.
5. **The container lacked the sanitizer runtime**; `scripts/linux-check.sh`
   installs it.

After the fixes the run passes on both hosts with no fixture excepted, so
the list of known reports (`sanitizedKnown`) is empty and RC1's leak, which
no fixture exercises yet, will be covered by its own fixture in phase 3.
`TestSanitizerDetects` proves the instrumentation is on: a program that
reads freed memory through raw pointers must end in a
`heap-use-after-free` report.

Items 2 and 3 change observable behaviour: destructors now run where they
did not. Every fixture's expected output was unaffected, because none
depended on a leaked value's destructor not running.

## Requirement to test

| Clause | Test |
|---|---|
| RA1: one translation unit of includes; every entry declared in the header | `TestRuntimeWarningClean` (`-Wmissing-prototypes` fails an undeclared entry); `TestSourceAmalgamates` |
| RA1: the cache follows an edit to any unit or header | `TestSourceAmalgamates` (the cache key is `hosted.Source`, which holds every file's text); the cache tests of `compiler/backend/native` |
| RA2: hosted, debug and release, with and without test hooks | `TestRuntimeWarningClean` (eight hosted compiles) |
| RA2: freestanding, both triples | `TestRuntimeWarningClean` (eight freestanding compiles) |
| RA3: definitions read from clang's IR | `TestRuntimeABI`, `TestParseABI` |
| RA3: every declaration names a defined entry and matches it | `TestRuntimeABI` (hosted: must be defined) |
| RA3: both architectures of the host's system | `TestRuntimeABI` (`hostedTriples`); on Linux the second architecture is skipped when its headers are absent |
| RA3: unreferenced entries are listed with a reason | `TestRuntimeABI` (both lists are empty) |
| RA4: the symbol is defined and referenced; another version does not link | `TestRuntimeABIVersion`; `TestEmitFunctionMarkers` (the entry's shape) |
| RA5: the sanitizer mode, the fixture run, no report | `TestFixturesSanitized`, `TestSanitizerDetects` (with `KERN_SANITIZE=1`) |
| RA5: a CI job on Linux | `.github/workflows/ci.yml`, job `sanitize` (`actionlint` clean; not yet run on GitHub) |
| RA6: the analyzer reports nothing | `TestRuntimeStaticAnalysis` |

## Deviations and open points

- **RA3, the second Linux architecture.** The spec asks for both
  architectures of the host's operating system. On Linux that needs the
  other architecture's system headers; the test checks it when they are
  installed and logs a skip otherwise. CI runs Linux on both architectures
  natively, so both are checked there.
- **RA5 is not complete until gate 8**, when the run is repeated over the
  fixtures the later phases add.
- **The CI `sanitize` job** has not run on GitHub: nothing is pushed.
- **`tests` do not use `kern_run_main` yet.** A `kern test` executable
  calls its test directly after `kern_start`; phase 3 moves it behind the
  same call when it gives `main` a configured stack.

## Verification

- `go test ./...` passes on macOS; `golangci-lint run ./...` reports no
  issue.
- In the Linux container: the ABI, warning, version and analyzer tests,
  `./runtime/...`, `./compiler/backend/...`, and the sanitized fixtures
  with the leak checker.
- The fixture harness runs every fixture in the interpreter and natively
  at debug and release; all agree after the two compiler fixes.
