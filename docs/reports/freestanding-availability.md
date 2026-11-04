# Hosted-only runtime entries in a freestanding program

**Date:** 2026-11-03. **Spec:** `specs/freestanding-availability/SPEC.md`.
**Plan:** `specs/freestanding-availability/PLAN.md`. Its two phases are one
commit: the code `ENV_HOSTED_ONLY` must be registered before the analysis
can build a diagnostic, and a registered `lang` code needs its fixture
pair, which is the second phase's. **Baseline:** the compiler at 7071ca3.

## What was built

- **The rule** (Rule 6.8, `docs/ownership.md`). In freestanding mode,
  `check`, `build` and `run` report `ENV_HOSTED_ONLY` at each call in the
  program's own source that reaches a runtime entry the freestanding
  runtime does not define, and exit 2 before anything is compiled to
  native code, linked, written or executed:

  ```
  call to `time.now` needs the hosted runtime (`kern_now_ns`), which a freestanding image does not have
  ```

  Before, `check` accepted the program, `build` failed with an `INTERNAL`
  linker error, and `run` executed it on the interpreter's emulation.
- **Recording.** `llvm.EmitRecorded` emits a module and returns, instead
  of the text, every request to declare a runtime entry (`kern_…`), with
  the IR instruction that made it, before deduplication. A recording also
  emits the functions the inliner copied everywhere, whose calls are
  still the program's. `EmitWith` is unchanged: the 36 freestanding
  images and every check output compared below are byte-identical.
- **The provided set.** `freestanding.Provided()` reads the external
  definitions of the embedded `rt.c` and startup sources.
  `TestProvidedMatchesObjects` checks it against `llvm-nm` of the compiled
  objects for both targets, and it matches exactly.
- **The analysis.** `analysis.HostedOnly` builds the library call graph
  from calls and function references, takes the fixed point of what each
  library function reaches, and reports, from the program's own
  functions:
  - each direct hosted-only request;
  - each call or reference to a library function that reaches one.

  Calls at one span are merged with the union of their entries. A call
  with no source position takes its function's declaration span.
  `ir.FuncRef` gained a `Pos` for this.

## Verification

- **R1.** For `time.now()`, a direct `now_ns()`, and `time.sleep` inside a
  function of the program, `check`, `build` and `run` give exit code 2
  with one diagnostic each, at both levels. `build` writes nothing and
  never calls the native build, and `run` never starts the interpreter.
  The same programs in hosted mode check, run and build as before. The
  analysis tests also cover:
  - a function value of `time.now`, reported at the reference;
  - a closure of the program's own, reported at the call inside it;
  - an uncalled function and a destructor;
  - a call under `if false`, which is reported because the rule reads
    the compiler's IR;
  - a generic function's instantiations, merged into one diagnostic;
  - a library cycle;
  - a call without a position.
- **R2.** Every freestanding fixture of the baseline corpus is accepted
  in its environment and runs as before. Each of the 9 fixtures was
  built with both compilers at both levels for both targets: 36 images,
  all byte-identical.
- **R3.** `Provided()` contains every symbol of ADR-011's table and equals
  the compiled objects' defined external symbols for both targets.
- **R4.** For R1's three programs, the analysis's entries equal the
  symbols `ld.lld` reports undefined, on both targets at `--opt=debug`
  (`TestHostedOnlyMatchesLinker`).
- **R5.** `ENV_HOSTED_ONLY` is registered under rule 6.8, category `lang`,
  in `codes.go`, `spec/rules.md` and `spec/rules.lock`. The fixture pair
  is `tests/freestanding/hosted_only.kern` and `hosted_only_ok.kern`. The
  harness now checks a reject fixture in its header's environment.
- **R6.** `check` on the 444 budget-free hosted files under `tests/` gives
  output byte-identical to the baseline compiler's, and a counting test
  shows that hosted `check` never enters the evaluation.

## Found on the way

- **Equality glue** (c88eea5, its own commit). The inventory over the
  corpus crashed the emitter on tasks-v1 t07's reference: the glue for a
  union with a struct payload copied its builder by value (a Go panic),
  and then rendered the struct's field address through a format string
  that the `%` of the LLVM type name broke. The interpreter also had no
  `==` for arrays. All three are fixed, with `tests/run/eq_glue.kern`.
- **Floats were hosted.** Formatting and parsing a float
  (`String.push_f64`, `str.parse_f64`, `json.as_f64`,
  `json.number_f64`) reached `kern_str_f64_digits` or
  `kern_str_parse_f64`, which the freestanding runtime did not define, so
  such a program was rejected at the call. *Since 2026-11-04*
  (`specs/freestanding-float`, `docs/reports/freestanding-float.md`) the
  freestanding runtime defines both, and the inventory below no longer
  lists the four functions.
- **Robustness.** `check --env freestanding` over the 184 single-file
  programs of the corpus at both levels (368 checks) gave no internal
  error. 116 of the checks report `ENV_HOSTED_ONLY`, from hosted programs
  that use the clock, files or processes.

## Library functions that reach the hosted runtime (R7)

The list covers the library functions the corpus compiles, not every
function of `std/`: the programs under `bench/kern/`, `examples/`,
`tests/run/`, `tests/trap/` and `tests/positive/`, and the agent suites'
references. `TestHostedOnlyInventory` generates it and fails when it is
stale.

<!-- hosted-only inventory: generated by TestHostedOnlyInventory -->
243 of 243 corpus programs lowered, at `--opt=debug`.

The two freestanding targets give the same list.

| library function | hosted-only entries it reaches |
|---|---|
| `Child.destructor` | `kern_proc_close`, `kern_proc_release` |
| `Child.kill` | `kern_proc_signal` |
| `Child.signal` | `kern_proc_signal` |
| `Child.take_fd` | `kern_proc_errno` |
| `Child.take_stderr` | `kern_proc_errno` |
| `Child.take_stdin` | `kern_proc_errno` |
| `Child.take_stdout` | `kern_proc_errno` |
| `Child.terminate` | `kern_proc_signal` |
| `Child.try_wait` | `kern_proc_wait` |
| `Child.wait` | `kern_proc_close`, `kern_proc_flush_inherited`, `kern_proc_wait` |
| `Command.check` | `kern_proc_errno` |
| `Command.child_env` | `kern_proc_env_at`, `kern_proc_env_count` |
| `Command.output` | `kern_join_drop`, `kern_join_get`, `kern_proc_close`, `kern_proc_env_at`, `kern_proc_env_count`, `kern_proc_errno`, `kern_proc_fault_take`, `kern_proc_flush_inherited`, `kern_proc_read`, `kern_proc_spawn`, `kern_proc_wait`, `kern_proc_write`, `kern_scope_drop`, `kern_scope_new`, `kern_scope_spawn` |
| `Command.spawn` | `kern_proc_env_at`, `kern_proc_env_count`, `kern_proc_errno`, `kern_proc_spawn` |
| `Command.spawn_modes` | `kern_proc_env_at`, `kern_proc_env_count`, `kern_proc_errno`, `kern_proc_spawn` |
| `Command.status` | `kern_proc_close`, `kern_proc_env_at`, `kern_proc_env_count`, `kern_proc_errno`, `kern_proc_flush_inherited`, `kern_proc_spawn`, `kern_proc_wait` |
| `Deadline.expired` | `kern_now_ns` |
| `Deadline.remaining` | `kern_now_ns` |
| `Instant.elapsed` | `kern_now_ns` |
| `Pipe.destructor` | `kern_proc_close` |
| `Pipe.read` | `kern_proc_errno`, `kern_proc_fault_take`, `kern_proc_read` |
| `Pipe.read_all` | `kern_proc_errno`, `kern_proc_fault_take`, `kern_proc_read` |
| `Pipe.write_all` | `kern_proc_errno`, `kern_proc_fault_take`, `kern_proc_write` |
| `VecIn.seed_len` | `kern_test_require_hooks` |
| `io.args` | `kern_io_arg`, `kern_io_argc` |
| `io.kind_of` | `kern_io_kind` |
| `io.list_dir` | `kern_io_list` |
| `io.make_dir` | `kern_io_mkdir` |
| `io.make_temp_dir` | `kern_io_temp` |
| `io.make_temp_file` | `kern_io_temp` |
| `io.read_file` | `kern_io_close`, `kern_io_open`, `kern_io_read` |
| `io.read_from` | `kern_io_read` |
| `io.remove_dir` | `kern_io_rmdir` |
| `io.remove_file` | `kern_io_remove` |
| `io.temp_path` | `kern_io_temp` |
| `io.write_all` | `kern_io_write` |
| `io.write_file` | `kern_io_close`, `kern_io_open`, `kern_io_write` |
| `process.env` | `kern_proc_env_at`, `kern_proc_env_count` |
| `process.env_entry` | `kern_proc_env_at` |
| `process.errno_err` | `kern_proc_errno` |
| `process.fail_next_io` | `kern_proc_fault_arm` |
| `process.gather` | `kern_join_drop`, `kern_join_get`, `kern_proc_errno`, `kern_proc_fault_take`, `kern_proc_read`, `kern_proc_write`, `kern_scope_drop`, `kern_scope_new`, `kern_scope_spawn` |
| `process.is_epipe` | `kern_proc_errno` |
| `process.read_end` | `kern_proc_errno`, `kern_proc_fault_take`, `kern_proc_read` |
| `process.write_end` | `kern_proc_errno`, `kern_proc_fault_take`, `kern_proc_write` |
| `time.after` | `kern_now_ns` |
| `time.now` | `kern_now_ns` |
| `time.sleep` | `kern_time_sleep_ns` |
| `time.wall_nanos` | `kern_time_wall_ns` |
<!-- end of the hosted-only inventory -->
