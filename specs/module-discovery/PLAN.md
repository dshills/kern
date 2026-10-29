# Plan: module discovery and the project manifest

**Source spec:** `specs/module-discovery/SPEC.md` v0.7 (2026-10-29),
speccritic VALID_WITH_GAPS with no critical finding. **Plan gate:**
plancritic (profile davin-go, with the spec) EXECUTABLE_WITH_CLARIFICATIONS,
no critical finding, no open question; its last warnings (top-level
`null`, the second decode's error text, `output` validation, the
injected working directory) are answered in §0. **Conventions:** every
phase ends with `go test ./...` and `golangci-lint run ./...` clean, a
`prism review staged` with no high or medium finding left unanswered, and
one conventional commit on `main`. Requirement labels (R1–R29) and
acceptance labels (A1–A7) are the spec's.

## 0. Decisions that apply to every phase

- **Package.** Discovery and the manifest are one new package,
  `compiler/project`, importing `compiler/source`, `compiler/parser`,
  `compiler/ast` and `compiler/diag` and nothing from `cmd/`. (R28.)
- **Prologue parsing.** Phase 1 adds `parser.ParsePrologue(f
  *source.File) (module string, imports []*ast.Import, ds
  []diag.Diagnostic)` to `compiler/parser`: it runs the existing
  `module` and `import` productions and stops at the first declaration
  without parsing it, so discovery reads the whole file's bytes (R13:
  the contents are returned) but parses only the prologue. A prologue
  that fails yields the same `SYNTAX` diagnostic the full parse yields
  for it (the same production, same position) and no imports. (R13.)
- **Identity of files.** Two names are the same file when
  `filepath.Clean` of each name made absolute against the working
  directory is equal; no symbolic link is resolved. (§2, R8, R15.)
- **Names of discovered files.** `filepath.Join(root, path, name)` with
  `root` as the walk found it: the first named file's directory spelled
  as given, or an ancestor of it, or `"."`-relative for the working
  directory fallback (spelled as the working directory's own relative
  form, `""` joined to the import path, so that a fallback root gives
  `lib/lib.kern`). (R9, A6.)
- **The working directory.** `Load` takes `cwd` explicitly: every
  relative name (a named file, a root reached by the walk, a discovered
  file) is accessed on disk as `filepath.Join(cwd, name)` while the name
  itself is kept as the display name in diagnostics and results, so a
  test may inject a temporary directory as `cwd` without `os.Chdir`;
  `cmd/kern` passes `os.Getwd()`. (R4, R9.)
- **Diagnostics.** Discovery returns `[]diag.Diagnostic` built with
  `diag.New`; the CLI delivers them through the existing single-response
  path, so exit statuses and `--human` need no new code. (R26.)
- **Diagnostic codes.** `MODULE_NOT_FOUND` rule `T.1` category `lang`;
  `PROJECT_NOT_FOUND` rule `P.1` and `PROJECT_INVALID` rule `P.2`,
  category `cli`; all phase 0 (always registered). (§6.)
- **Fixture coverage of `MODULE_NOT_FOUND`.** As R25 and the §3 non-goal
  allow: the harness's coverage table `generated`
  (`tests/harness_test.go`) gains `MODULE_NOT_FOUND: TestModuleNotFound`,
  a test in the `tests` package that runs discovery on a temporary
  directory for the reject case and its accepting twin; a CLI contract
  test covers the code through `kern check` too. (R25.)
- **Discovery traversal.** Breadth-first over the set in order; a
  module directory is entered once (a visited set keyed by the cleaned
  absolute directory path) when it succeeds, and a failure is never
  recorded in the visited set, so every import declaration naming a
  failing path reports its own `MODULE_NOT_FOUND` (R11). A module
  directory's files are the entries of `os.ReadDir` whose name ends in
  `.kern` and whose mode is regular (a symbolic link is followed by
  `os.Stat` and counted when its target is a regular file), in the
  byte order `os.ReadDir` returns; subdirectories are never entered on a
  module's account (R9, §2). Named files are never deduplicated against
  each other (R8).
- **Manifest decoding.** The sequence, in order: (1) `os.ReadFile`; an
  error is `PROJECT_INVALID` quoting it. (2) Bytes that are empty or
  only JSON whitespace are the empty manifest. (3)
  `json.NewDecoder(bytes.NewReader(b)).Decode(&raw)` with `raw` a
  `map[string]json.RawMessage`: a `*json.UnmarshalTypeError` (the value
  is not an object) is `PROJECT_INVALID` "kern.mod must hold a JSON
  object", and so is a nil map after a successful decode (the value was
  `null`); any other error is `PROJECT_INVALID` quoting the error's text;
  a repeated key leaves its last value in the map (R21). (4) A second
  `Decode` into a throwaway value must return `io.EOF`; a syntax error
  there is `PROJECT_INVALID` quoting it, and any other outcome is
  `PROJECT_INVALID` "trailing content after the manifest object" (R22).
  Each known field is then validated from its raw value: a JSON string
  for `schema_version`, `opt`, `env`, `target`, `output`, with `null` and
  every other type rejected as "wrong type"; `entry` a string or a
  non-empty array of strings, each a relative path with no `..`
  component; `output` a relative path (not absolute); the set
  checks of §5; an unknown key is "unknown field".
  Every message names the field. The result is `Manifest` with one
  `*string` per scalar field and `Entry []string`, nil meaning unset.
  (R21, R22.)

## Phase 1 — The `project` package

**Goal.** A Go API that finds the root and manifest, validates the
manifest, and discovers the file set, with unit tests for every rule.

**Files.** `compiler/parser/prologue.go` (`ParsePrologue`, with a test in
`compiler/parser` that compares its module and imports with `Parse`'s on
every fixture under `tests/` whose full parse succeeds, and that compares
its `SYNTAX` diagnostic, code and span, with `Parse`'s on dedicated
malformed-prologue inputs: a missing module line, an unterminated import
string, an import without a newline); `compiler/diag/codes.go` and
`spec/rules.lock` (the three codes of §6, added together with the first
code that emits them); `compiler/project/manifest.go` (type `Manifest` with
`SchemaVersion, Opt, Env, Target, Output *string` and `Entry []string`,
`func ParseManifest(path string, b []byte) (*Manifest, []diag.Diagnostic)`);
`compiler/project/root.go` (`func FindRoot(cwd string, named []string)
(root string, manifest string, found bool)`, the R2/R3 walk, inspection
errors treated as absence); `compiler/project/discover.go` (`type
Result struct { Root string; Manifest *Manifest; Files []*source.File;
Diags []diag.Diagnostic }` and `func Load(cwd string, named []string)
Result`, which applies R3, R6, R8–R14: validates the manifest, loads the
named files or the manifest's `entry`, then the breadth-first discovery
over user import paths with a visited set of module directories and a
seen set of file identities); `compiler/project/project_test.go`.

**Key decisions.** A missing named file is returned as the existing
`API_FILE_NOT_FOUND`, so the CLI's current message survives; the walk
stops at the filesystem root as `filepath.Dir` reports it; the user
import path check (§2) is a pure function `validImportPath(string)
bool` with its own table test.

**Tests (unit, temp directories).** Root found in the file's directory,
in an ancestor, fallback to cwd, an ancestor that cannot be read, no
file and no manifest (R2, R3); entry
from the manifest as string and as array, and `PROJECT_INVALID` without
one (R6); each A3 manifest case plus an empty file and a whitespace-only
file (R21, R22); discovery of a one-level and a nested import
(`lib/inner`), ordering (R14), a file both named and imported read once
(R8), an entry directory imported by another module (R7), a cycle (R12),
a missing module, a malformed path, an empty directory, an unreadable
file, and two missing imports reported both (R11), a wrong module name
in a discovered directory (R10), a prologue syntax error (R13), that
naming every file adds nothing and that two spellings of one file are
one file (R15), a named file given twice read twice (R8), a module
directory holding a subdirectory and a non-`.kern` file that are ignored
(R9), and that discovery opens nothing outside the root, the named files
and the walk (R29: a test with a file outside the root that is only
reachable by a `..` import path, rejected as malformed).

**Acceptance.** `go test ./compiler/project` passes with every listed
case; `go vet` and lint clean; no import of `cmd/kern`.

**Risks.** Spelling of the fallback root: the test asserts
`lib/lib.kern` exactly for a cwd fallback and `fixtures/x/lib/lib.kern`
for a root reached from `fixtures/x/app.kern`.

## Phase 2 — Coverage of the codes in the fixture harness

**Goal.** The three codes, registered in Phase 1, are covered as the
harness requires.

**Files.** `tests/harness_test.go` (the
`generated` map entry), `tests/project_test.go` (`TestModuleNotFound`:
a temporary program importing `nowhere` yields exactly one
`MODULE_NOT_FOUND` at the import's span, and the same program with the
module present yields none; this is the reject/accept pair the harness
rule asks for).

**Acceptance.** `go test ./tests -run 'TestRulesLock|TestFixtures|
TestModuleNotFound'` passes; `go test ./...` passes.

**Risks.** The rules-lock test compares the registry and the lock both
ways, which Phase 1 satisfies by adding the three together; the harness's
coverage check fails from Phase 1 until this phase lands, so the two
phases are committed together when Phase 1's commit would otherwise
leave `go test ./tests` red.

## Phase 3 — Command-line and machine-API integration

**Goal.** Every command applies discovery and the manifest as §4.4 and
§5 require, with contract tests for A1–A7.

**Files.** `cmd/kern/main.go`: after flag parsing and before `source.Load`,
call `project.Load(cwd, files)`; on `PROJECT_*` or discovery diagnostics
return the single response (exit 2); otherwise use `Result.Files` as
`srcs` and `Result.Files`' names as `req.Files` for `check`'s
`result.files` (R18) while `format` keeps the entry files, the named
ones or the manifest's `entry` (R19); a request `span` is checked
against the discovered set's names, as it is checked against the request
files today (R18); apply the manifest's `target`/`opt`/`env` when the CLI and the
request did not set them (R23); `--cache` receives the discovered list
(R20); drop the `API_NO_FILES` check for an empty `files` when a manifest
supplies `entry` (R17). `cmd/kern/run.go` (`parseRunArgs` no longer
requires a file; `runProgram` calls `project.Load`; the interpreter's
program name stays `files[0]` of the entry set) and `cmd/kern/build.go`
(same; `-o` defaults to the manifest's `output` resolved under the root;
missing both stays `CLI_INVALID_FLAGS`, R24). `cmd/kern/cli_test.go`:
contract tests that copy `tests/positive/import_call` into a temporary
directory, change into it with `t.Chdir`, and run `run`, `build` and
`check` with the entry only, for `import_call`, `import_iface`,
`import_funcval`, `import_const_array` and `import_trap` (A1), with an
empty `kern.mod` from the parent directory (A2), with `{"entry":
"app.kern"}` and no file (A2), from the `lib` subdirectory (A2), each A3
manifest (A3), `PROJECT_NOT_FOUND` (A3), `MODULE_NOT_FOUND` and
`MODULE_MISMATCH` through `kern check` and a program with two missing
imports reporting both (A4), the diagnostics of a program with an error
in `lib` equal, in order and text, between naming the entry only and
naming every file (A4), `--opt=release`, `--env=freestanding` and
`--target` overriding the manifest, and a CLI `--target` that differs
from a request `target` still yielding `API_TARGET_CONFLICT` with a
manifest present (A5, R23), the trap fixture's record
naming `lib/lib.kern` and `show-bounds-checks` locations naming
`lib/lib.kern` (A6), `explain-ownership` with a span in `lib/lib.kern`
accepted and a span in an unnamed file rejected with `API_INVALID_SPAN`
(R18), and `format` versus `check` file lists (A7). The usage line in `main.go`
gains `[files...]` optional wording (R27, with Phase 4's docs).

**Key decisions.** The CLI calls `os.Getwd()` once and passes it to
`project.Load`, so that tests can set the working directory; `fail`
remains the single response writer; discovery diagnostics are returned
as a batch through the existing diagnostics response so that several
`MODULE_NOT_FOUND` are reported together (R11).

**Acceptance.** A1–A7 as contract tests; `go test ./...` unchanged in
outcome elsewhere (A5); `kern run tests/positive/import_call/app.kern`
from the repository root reports `MODULE_NOT_FOUND` (the fallback root
is the repository root, where `lib/` does not exist), which the report
documents as the behaviour `spec/types.md` prescribes.

**Risks.** `t.Chdir` requires Go 1.24; the module's `go.mod` is checked
first and `os.Chdir` with a deferred restore is used if it is older.
Tests that already run `kern` on fixture paths from the `cmd/kern`
directory keep working because every such invocation names every file
(R15).

## Phase 4 — Documentation and report

**Goal.** Users and agents learn the rule from the places they read.

**Files.** `README.md` (one paragraph under the running and building
sections: name the entry file, imports are found from the project root,
`kern.mod` marks the root and may hold `entry`, `opt`, `env`, `target`,
`output`; an example of a two-module program), `docs/ownership.md` (one
paragraph after the import paragraph, the same content in the
reference's register), `cmd/kern/main.go` usage line (two lines: the command syntax with
`[files...]`, and "files default to the entry of kern.mod, the manifest
that marks the project root and may set entry, opt, env, target and
output"), and
`docs/reports/module-discovery.md` (what changed, the open decision on
R7 with the proposed `spec/types.md` sentence for the owner, the
fallback-root behaviour and its consequence for running fixtures from
the repository root, and the acceptance evidence).

**Acceptance.** R27's three paragraphs exist (the usage line names the
manifest and the optional file list: `usage: kern <command> [flags]
[files...]   (files default to kern.mod's entry)`); `go test ./...` and
lint clean (docs only, prism skipped). The report is supporting
documentation, not a spec deliverable.

## Traceability

| requirement | phase |
|---|---|
| R1–R4 root | 1 |
| R5–R8 entry files | 1 (API), 3 (CLI) |
| R9–R15 discovery | 1 |
| R16–R20 commands | 3 |
| R21–R24 manifest | 1 (parse, validate), 3 (precedence, `-o`) |
| R25 codes and coverage | 2 |
| R26 delivery | 3 |
| R27 docs | 4 |
| R28–R29 API and reads | 1 |
| A1–A7 | 3 (contract tests), 4 (report) |
