# Spec: the v0.1.0 release

**Version:** 0.6. **Status:** draft. **Date:** 2026-11-08.

**Terms.** *README* is `README.md` at the repository root. *PATH* is the
shell's executable search path. *CI* is the `ci` workflow in
`.github/workflows/ci.yml`. *QEMU* is the machine emulator that runs
freestanding images. *ADR* is an architecture decision record in
`docs/decisions/`. *Gate* is one check in §3 that must pass locally.

## 0. Goal

Make kern releasable as `v0.1.0`, a research preview, and leave the
release one `git push` of a tag away. Today the compiler builds reproducibly
for four targets (`scripts/release.sh`, `scripts/check-reproducible.sh`) and a
binary built outside the repository runs programs with only `clang` on the
PATH, because the standard library and the runtimes are embedded. What is
missing:

- the binary cannot say what version it is;
- the repository has no tags, so `release.sh` names archives after a commit;
- nothing publishes a release when a tag is pushed;
- there is no changelog and no statement of what is stable;
- the README has no install section, and its quick-start program writes no
  output (it reports its result as exit code 21).

The version is `0.1.0`, not `1.0.0`. The machine API's schema moved from 0.3
to 0.5 in one week, the language specification is a v1.0 candidate that
gained a feature last week (ADR-016), and the agent experiment has not met
its own threshold. A 0.x version promises no stability that is not needed.

## 1. Non-goals

- Pushing the tag or publishing the release. Both are the owner's action.
- Signed releases. Releases stay checksummed, not signed
  (`docs/security-review.md`).
- Package-manager channels (Homebrew, apt), Windows builds, and a hosted
  documentation site.
- Any change to the language, the compiler's output, the standard library or
  the machine API's existing commands and schema versions.
- A 1.0 stability promise.

## 2. Requirements

**R1. Version resolution.** The `kern` binary resolves a version string and
a commit string once, as follows.
- *Version.*
  1. The value of the package variable `main.version` when the build set
     it with `-ldflags "-X main.version=<v>"` to a non-empty string.
  2. Otherwise the main module's version from `runtime/debug.ReadBuildInfo`,
     when it is neither empty nor `(devel)`. This is the case for
     `go install github.com/dshills/kern/cmd/kern@v0.1.0`, and for
     `go build` in a git checkout, which since Go 1.24 stamps the tag on a
     tagged commit and otherwise a pseudo-version such as
     `v0.0.0-20261104220558-782404bdd594+dirty`.
  3. Otherwise the string `devel`.
- *Commit.* The build setting `vcs.revision`, cut to its first 12
  characters, followed by `-dirty` when the build setting `vcs.modified` is
  `true`. The empty string when there is no `vcs.revision` (for example a
  `go install` from the module proxy).

The resolution is a pure function of (linked version, build info), so it is
unit-tested without building a binary.

**R2. `kern version`.** A new CLI command, dispatched before request parsing
as `run`, `build`, `test` and `lsp` are. It is not a §7 machine-API command;
the schema's command enum is unchanged.
- `kern version` writes one JSON response on standard output and exits 0:
  `{"schema_version":"0.5","ok":true,"result":{...},"diagnostics":[]}`.
  `result` holds `version`, `commit`, `go` (`runtime.Version()`), `os`,
  `arch` (`runtime.GOOS`, `runtime.GOARCH`) and `schema_versions`, the list
  `diag.Accepted` in its order. Every key is present; `commit` is `""` when
  R1 resolves no commit.
- `kern version --human` writes one line and exits 0 (`--human` given more
  than once is the same as once, as for the other commands):
  `kern <version> (<commit>, <go>, <os>/<arch>)`, with `<commit>, ` left out
  when the commit is empty.
- `kern --version` behaves exactly as `kern version --human`.
- Any other argument to `version` (a flag or a file) is a
  `CLI_INVALID_FLAGS` diagnostic response with exit code 2, as for the other
  commands. `kern --version` with further arguments is the same error.
- The usage line printed by `kern` with no arguments lists `version`.

**R3. Release builds carry the version.** `scripts/release.sh <version>`
links `-X main.version=<version>` into every binary, and each archive holds
`LICENSE` and `CHANGELOG.md` beside `README.md`. `scripts/check-reproducible.sh`
builds with the same flags, version included, so it checks what ships. Both
scripts take the version the same way (argument, default `git describe
--always --tags --dirty`).

**R4. Release check script.** `scripts/release-check.sh <tag> <notes-file>`
does the checks that need no network, so that the workflow and a person
preparing a release run the same code:
- the tag is `v` followed by a Semantic Versioning 2.0.0 version without
  build metadata, checked with the regular expression
  `^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?$`
  (no leading zeros in numeric components or numeric prerelease
  identifiers, no empty identifiers);
- `CHANGELOG.md` has a heading line `## [<tag>] - YYYY-MM-DD`;
- the text of that section, from the line after its heading up to the next
  `## ` heading or the link-reference block at the end, is non-empty after
  trimming, and is written to `<notes-file>`.
Any failure prints the reason to standard error and exits 1.

**R5. Release workflow.** `.github/workflows/release.yml` runs on a push of
a tag matching `v*`, with `permissions: contents: write`, on
`ubuntu-latest` (GNU tar is present there). In order:
1. Check out the tag with full history; set up Go from `go.mod`.
2. `scripts/release-check.sh "$TAG" notes.md`.
3. Require the tagged commit to be an ancestor of `origin/main`.
4. Require a successful run of the `ci` workflow for the tagged commit on a
   push event (`gh run list --workflow ci.yml --commit <sha> --event push
   --status success`). The workflow does not rerun the test suite.
5. `scripts/check-reproducible.sh "$TAG"`, then `scripts/release.sh "$TAG"`.
6. Unpack the `linux-amd64` archive and require `kern version --human` to
   start with `kern <tag> (`.
7. `gh release create "$TAG" --draft` with every `dist/*.tar.gz` and
   `dist/SHA256SUMS`, title `kern <tag>`, notes from `notes.md`, and
   `--prerelease` exactly when the tag has a `-` suffix.
8. `gh release edit "$TAG" --draft=false`, which makes the release public.

A failure in steps 1 to 6 stops the workflow with nothing created. Step 8
runs only after step 7 reports success, so the workflow never asks for a
release to be made public before all its assets are uploaded. When step 7 or
8 fails, the remote state is uncertain (a request may have taken effect
before the failure was reported), and `docs/releasing.md` gives the
recovery, decided by `gh release view "$TAG"`:
- no release: rerun the workflow;
- a draft: delete it with `gh release delete "$TAG"`, then rerun;
- a public release with all five assets (four archives and
  `SHA256SUMS`): nothing to do;
- a public release missing any asset: delete the release (the tag stays)
  with `gh release delete "$TAG"`, then rerun the workflow, so that every
  asset and `SHA256SUMS` come from one build. Assets are never uploaded by
  hand.
`actionlint` reports no finding on the workflow file.

**R6. Changelog.** `CHANGELOG.md` at the root, in the Keep a Changelog
layout, with a `## [v0.1.0] - <date>` section that summarizes what the
repository contains at the release, grouped by area: language, compiler and
analyses, standard library, runtimes and targets, tooling (CLI, machine API,
language server, `kern test`, debugging), performance, and known
limitations. Every claim is backed by a file or report in the repository.
The section ends with a stability note matching R7. A link-reference block
at the end points `[v0.1.0]` at the GitHub release tag URL.

**R7. Stability statement.** The README gains a "Versioning and stability"
section that states the following four points (the quoted permissions are
the text's content, not optional behaviour of this work):
- releases follow semantic versioning in its 0.x form: a minor version may
  change the language, the standard library, the CLI and the machine API,
  and a patch version does not intend to;
- diagnostic codes are frozen to rule numbers in `spec/rules.lock`; a code
  is never reused for another rule, and any change to the file is listed in
  the changelog;
- the machine-API versions a release answers are the `schema_versions`
  list in the `kern version` result, and a release that drops one says so
  in the changelog;
- the language specification is a v1.0 candidate and may still change.

**R8. Install and quick start.** The README gains an "Installing" section
before "Building": a release archive (download, check against `SHA256SUMS`,
unpack, put `kern` on the PATH), `go install
github.com/dshills/kern/cmd/kern@latest`, and building from source. It
states the external requirements once (clang for native builds, `ld.lld` and
QEMU for freestanding images, glibc 2.34 on Linux). The quick start runs a
new example, `examples/hello/hello.kern`, which prints `hello, kern` and
exits 0, with an expected-output file so the fixture harness checks it. The
README's commands table lists `version`, and its ADR count says sixteen.

**R9. Releasing procedure.** `docs/releasing.md` lists the steps a release
takes: update `CHANGELOG.md`, run `scripts/release-check.sh` and
`scripts/check-reproducible.sh`, commit, push `main`, wait for CI, create an
annotated tag, push the tag, and check the published release (archives,
`SHA256SUMS`, `kern version` of a downloaded binary).

**R10. The tag.** The annotated tag `v0.1.0` is created locally on the
release commit after every gate in §3 passes on that commit. Those gates are
all local; the checks in R5 run only after the owner pushes the tag and are
not preconditions of creating it. The tag is not pushed.

## 3. Acceptance criteria and tests

Each item is a gate (R10).


- Unit tests for R1's resolution over: linked version set; module version
  `v0.1.0`; `(devel)` and empty module versions; revision with and without
  `vcs.modified`; no revision.
- CLI tests for R2: the JSON shape and keys, `schema_versions` equal to
  `diag.Accepted`, `--human` and `--version` output, the commit-less form,
  each rejected argument form, and the usage line.
- One test builds `cmd/kern` with `-ldflags -X main.version=v0.0.0-test` into
  a temporary directory and checks that `kern version --human` reports it.
- `scripts/release-check.sh`: run against `v0.1.0` (passes, notes non-empty),
  `0.1.0`, `v0.1` and a version with no changelog section (each fails with a
  reason).
- `actionlint .github/workflows/release.yml` is clean.
- A dry run of `scripts/check-reproducible.sh` and `scripts/release.sh` with a
  non-release version on a host with GNU tar (a Linux container when the Mac
  lacks it): four archives and `SHA256SUMS`, each archive holding `kern`,
  `README.md`, `LICENSE`, `CHANGELOG.md` and the runtime sources; the
  `linux-amd64` (or host) binary reports the version.
- `examples/hello` passes the fixture harness; `go test ./...`,
  `golangci-lint run ./...` and `gofmt -l` are clean; `go test ./tests`
  passes for the documentation changes.
  One fixture has a standing host exception: `race_raw_ptr` (a
  ThreadSanitizer fixture) times out on this Mac because the symbolizer
  hangs, at old commits too (`docs/reports/` and earlier plans). If
  `go test ./...` fails only there and only by timing out, the local gate
  passes, because the fixture is still enforced before publication: R5
  step 4 requires a successful CI run of the tagged commit, and CI runs the
  full suite, that fixture included, on Linux and macOS.
- Nothing else changes: `scripts/capture-ll.sh` output is not needed, since
  no compiler package changes; `git diff --stat` lists only `cmd/kern`, the
  scripts, `tests/release_check_test.go`, `.gitignore` (`/dist/`), the workflow, the docs, `CHANGELOG.md`, `README.md`,
  `examples/hello` and this directory.
