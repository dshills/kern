# Plan: the v0.1.0 release

Implements `specs/release-v0.1/SPEC.md` v0.5 in two phases, one commit each.
Phase 1 is code and automation; phase 2 is the documents, the example, the
release dry run and the local tag. No compiler package changes in either.

## Phase 1: version command, release scripts and workflow

**Goals.** R1 to R5.

**Files.**
- `cmd/kern/version.go` (new):
  - `var version string`, set only by `-ldflags "-X main.version=..."`.
  - `type buildIdentity struct{ Version, Commit string }` and
    `func resolveIdentity(linked string, bi *debug.BuildInfo, ok bool) buildIdentity`,
    the pure function of R1 (linked version, else module version unless
    empty or `(devel)`, else `devel`; commit from `vcs.revision` cut to 12,
    `-dirty` when `vcs.modified` is `true`).
  - `func currentIdentity() buildIdentity` reads `debug.ReadBuildInfo()`
    and calls `resolveIdentity`. It is called only from the two version
    entry points, at most once per process, so the identity is resolved
    once (R1).
  - `func versionCommand(args []string, id buildIdentity, stdout io.Writer) int`:
    accepts only `--human` (any number of times); otherwise `fail(stdout,
    "CLI_INVALID_FLAGS", ...)` with exit 2. Taking the identity as a
    parameter lets tests cover the commit-less form. JSON: a `diag.Response` at
    `diag.SchemaVersion` with `OK: true`, `Diagnostics: []diag.Diagnostic{}`
    and `Result` a struct with JSON keys `version`, `commit`, `go`, `os`,
    `arch`, `schema_versions`. Human: the one line of R2.
- `cmd/kern/main.go`: dispatch `version` beside `run`/`build`. `--version`
  as the first argument: with no further argument it runs
  `versionCommand(["--human"], currentIdentity(), stdout)`; with any
  further argument (including `--human` or a second `--version`) it is
  `CLI_INVALID_FLAGS`, exit 2 (spec R2). The usage line lists `version`.
- `cmd/kern/version_test.go` (new): the R1 table test, the R2 CLI tests
  through `run(...)` (JSON keys and values, `schema_versions` equal to
  `diag.Accepted`, `--human`, repeated `--human`, `--version`, the
  commit-less JSON and human forms through an identity with no commit,
  each rejected form: `version --json`, `version file.kern`, `--version
  extra`, `--version --human`, `--version --version`), the usage line, and
  one test that runs `go build -ldflags "-X main.version=v0.0.0-test"` into
  `t.TempDir()` and checks `kern version --human` (skipped under
  `testing.Short()`).
- `scripts/release.sh`: `-X main.version=$version` in `-ldflags`; copy
  `LICENSE` and `CHANGELOG.md` into each archive directory (a missing file
  is an error, not `|| true`).
- `scripts/check-reproducible.sh`: take the version as `release.sh` does and
  build with the same `-ldflags`.
- `scripts/release-check.sh` (new): R4, in bash with `set -euo pipefail`.
  Every check failure goes through one `die` function that prints
  `release-check: <reason>` on standard error and exits 1; usage errors
  (wrong argument count) do the same. The tag is matched with R4's
  regular expression via `[[ =~ ]]`. The heading must match
  `^## \[<tag>\] - [0-9]{4}-[0-9]{2}-[0-9]{2}$` (the tag's dots escaped).
  The section is extracted with `awk` from the line after that heading to
  the next `## ` line or the first link-reference line
  (`^\[[^]]+\]: `); empty after trimming whitespace fails; otherwise it is
  written to `<notes-file>` and the script exits 0. The changelog path is
  `$CHANGELOG`, default `CHANGELOG.md`.
- `tests/release_check_test.go` (new): runs the script (skipped without
  `bash`) against temporary changelogs: a valid tag and section (exit 0,
  notes file equal to the section), and each failing case with exit 1 and
  a `release-check:` line on standard error: tag forms `0.1.0`, `v0.1`,
  `v01.0.0`, `v0.1.0-`, `v0.1.0-01`; a missing section; a heading without a
  date; an empty section; a wrong argument count.
- `.github/workflows/release.yml` (new): R5's eight steps, with
  `permissions: { contents: write, actions: read }` (`actions: read` for
  listing CI runs). `GH_TOKEN:
  ${{ github.token }}` for the `gh` steps; `actions/checkout@v4` with
  `fetch-depth: 0`; `actions/setup-go@v5` with `go-version-file: go.mod`.
  The CI check counts runs with
  `gh run list --workflow ci.yml --commit "$GITHUB_SHA" --event push
  --status success --json databaseId --jq length` and fails at zero. The
  ancestor check is `git merge-base --is-ancestor "$GITHUB_SHA"
  origin/main`.

**Key decisions.**
- **`version` is a CLI command, not a §7 command.** It reads no request,
  so it sits with `run`, `build`, `test` and `lsp`; `schema.json` is
  unchanged. Its JSON output still has the `diag.Response` envelope so a
  tool reads it like every other response.
- **Build info as the fallback.** `go install ...@v0.1.0` stamps the module
  version, so that channel needs no linker flag.
- **Draft, then publish.** The workflow creates a draft with every asset and
  publishes it in a separate step (spec R5).
- **The workflow trusts CI** instead of rerunning the test suite, which
  needs clang 20, lld and QEMU and takes most of an hour.

**Steps and verification.**
1. Write the code; `go build ./...`, `go vet ./cmd/kern`, `gofmt -l`.
2. `go test ./cmd/kern -run 'Version|Usage'` (including the linked-version
   build test).
3. `actionlint .github/workflows/release.yml`; `bash -n` on the three
   scripts.
4. `go test ./tests -run ReleaseCheck` (temporary changelogs); phase 2
   also runs the script against the real `CHANGELOG.md`.
5. Internal code review of the diff; findings fixed or recorded.
6. Gates: `golangci-lint run ./...`, `gofmt -l .`, `go test ./...`. Commit.

**Acceptance.** Every test above passes, `actionlint` is clean, lint is
clean, and the review's findings are handled.

**Risks.**
- **`vcs.modified` counts untracked files**, so a local build in a tree with
  an untracked file reports `-dirty`. That is accurate and is documented in
  `docs/releasing.md` (release builds run in a clean CI checkout).
- **The workflow cannot be run before a tag is pushed.** `actionlint`, the
  shared check script and the local dry run of phase 2 cover everything but
  the `gh` calls; `docs/releasing.md` gives the recovery for those.

## Phase 2: changelog, README, releasing procedure, example, dry run, tag

**Goals.** R6 to R10, and the remaining checks of spec §3.

**Files.**
- `CHANGELOG.md` (new): header (Keep a Changelog, semantic versioning),
  `## [v0.1.0] - <commit date>` grouped as R6 lists, built from the reports
  in `docs/reports/`, the ADRs and `git log`; every bullet names its file or
  report. A stability paragraph matching R7. Link reference
  `[v0.1.0]: https://github.com/dshills/kern/releases/tag/v0.1.0`.
- `README.md`: "Installing" before "Building" (R8); quick start on
  `examples/hello`; "Versioning and stability" (R7); `version` in the
  commands table; "Fifteen" ADRs becomes "Sixteen"; the Status section
  states the release and keeps its existing facts.
- `docs/releasing.md` (new): R9's steps and R5's recovery table.
- `examples/hello/hello.kern` and `examples/hello/hello.out` (new): prints
  `hello, kern` and a newline, returns 0.

**Steps and verification.**
1. Write the example; run it with `kern run` and through the fixture
   harness (`go test ./tests -run 'TestFixtures/.*hello'`) and the format
   test (`go test ./compiler/format`).
2. Write the documents. `scripts/release-check.sh v0.1.0 <notes>` passes
   with non-empty notes; `0.1.0`, `v0.1`, `v01.0.0` and `v0.2.0` (no
   section) fail with reasons.
3. Dry run on a host with GNU tar (a Linux container through OrbStack, as
   the Mac has none): `scripts/check-reproducible.sh v0.1.0-dryrun` and
   `scripts/release.sh v0.1.0-dryrun`. Four archives and `SHA256SUMS`; list
   each archive's contents; run the `linux-<arch>` binary's
   `kern version --human` in the container. If no container runtime starts,
   the dry run uses Homebrew's `gnu-tar` on the Mac instead and runs the
   `darwin-arm64` binary's `kern version --human`; the commit message says
   which host ran it. Either way, every archive's listing must hold
   `kern`, `README.md`, `LICENSE`, `CHANGELOG.md`,
   `runtime-hosted-src/` and `runtime-freestanding-src/`.
4. Internal review of the documents against the repository (every
   changelog claim checked against its cited file).
5. Gates: `go test ./...` (which includes `./tests` for the documentation
   lint), `golangci-lint run ./...`, `gofmt -l .`. Commit.
6. `git tag -a v0.1.0 -m "kern v0.1.0"` on that commit. Not pushed.

**Acceptance.** Every gate of spec §3 passes on the tagged commit; `git
diff --stat` from before phase 1 lists only the files named in this plan.

**Risks.**
- **Changelog accuracy.** It summarizes two weeks and 210 commits; a claim
  without a backing file is removed rather than softened.
- **The TSan fixture** times out on this host (`race_raw_ptr`, symbolizer
  hang). Spec §3 states the exception: a timeout there alone does not block
  the local gate, because the release workflow requires a successful CI run
  of the tagged commit, which runs that fixture. The commit message records
  it if it happens. Any other failure blocks.
