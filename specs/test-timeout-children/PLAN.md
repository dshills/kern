# Plan: `kern test --timeout` stops a test's child processes

**Spec:** `specs/test-timeout-children/SPEC.md` v0.1. **Profile:** davin-go.
One phase, one commit on `main`, not pushed. No compiler code generation,
prelude or runtime change, so no IR baseline.

## Phase 1 (R1–R5)

**Files.** `compiler/testrun/run.go`, `compiler/testrun/testrun_test.go`;
`compiler/interp/process.go`, `compiler/interp/interp.go`, a test in
`compiler/interp`; the documents of R5.

1. `runNative`: `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`;
   `cmd.Cancel` sends SIGKILL to `-cmd.Process.Pid`.
2. `procEntry.reaped`, set in `procWait` (the one path behind both
   `wait` and `try_wait`) under `it.sh.mu` when `Wait4` collects the
   status; `Wait4` itself runs outside the lock, so a blocked wait never
   holds it. `Interp.KillChildren`, under the lock, marks the table
   killed and sends SIGKILL to every unreaped entry (ignoring `ESRCH` and
   other errors), then collects each signalled child's status on a
   goroutine. `procSpawn`, when it registers a child after the table is
   marked killed, signals and collects that child at once.
3. `runInterp`: on timeout, `it.Stop()` then `it.KillChildren()`.
4. Tests: the R1/R2 test (one source, both engines, `native` skipped
   without clang), polling `kill(pid, 0)` for up to 2 s; the R3 unit test.
5. Documents (including the SIGINT risk of spec §6), then review:
   `golangci-lint run ./...`, `KERN_REQUIRE_QEMU=1 go test
   ./...`, `prism review staged --paths 'compiler/**' --reasoning-effort
   medium`, `realitycheck check --max-tokens 65536`, `verifier analyze
   -mode offline`; clarion skipped. If a tool cannot run, the commit
   waits and the owner is told.
6. Commit `fix(test): kill a timed-out test's child processes`.

**Acceptance.** All tests green; documents updated; reviews clean or
justified; committed.
