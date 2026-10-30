# Module discovery and the project manifest

**Version:** 0.7 (2026-10-29). **Scope:** the `kern` command line
(CLI) and the §7 machine application programming interface (API).
**Language impact:** none. `spec/types.md` defines the project root (the
nearest ancestor directory of the first named file that contains a file
named `kern.mod`, else the working directory of the `kern` process) and
`import "path"` as the module at `path` relative to that root; this
document keeps both rules unchanged and specifies how the files of an
imported module are found and what `kern.mod` contains. **Related:**
`specs/initial/AI_Native_Systems_Language_SPEC.md` §7 (machine API),
`spec/types.md` lines 16–18 (project root, imports, name visibility),
`docs/reports/multi-module-sweep.md` (the gap this closes).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general, with
`spec/types.md` as context) on this version: VALID_WITH_GAPS, no critical
finding; the remaining warnings are the deterministic preflight's flags
on the upper-case tokens of this document (requirement and acceptance
labels such as R1 and A1, rule numbers such as T.1 and P.1, and the terms
in the glossary) and its reading of the words "invalid" and "wrong" in
A3's enumerated cases as vague, accepted as such; one SCOPE_LEAK on R13's
parenthetical guidance about parser reuse is accepted as guidance.

**Glossary.** CLI: the `kern` command line. API: the §7 machine interface
(one JSON request on standard input, one JSON response on standard output).
JSON: JavaScript Object Notation. LLVM: the native backend's compiler
infrastructure; a target triple names its platform. OS: the operating
system.

## 1. Problem

Every file of a program must be named on the command line or in a request:
`kern run app.kern` for a program that imports `lib` fails with
`NAME_UNRESOLVED` at each use of `lib` unless `lib/lib.kern` is also
named. The compiler already knows where the module lives: the import path
is a directory relative to the project root, and the files of a module are
the `.kern` files of one directory. This document requires the compiler to
find them, and gives `kern.mod` a content so that a project is built with
no arguments.

## 2. Definitions

- **Module directory.** The files of a user module are the regular files
  named `*.kern` directly in one directory, in byte order of their names.
  Files in subdirectories belong to other modules. (This is the existing
  rule behind `MODULE_MISMATCH`: every named file of a directory declares
  the same module name.)
  *Amended 2026-10-30* (`specs/test-command/SPEC.md` §2.1): a file whose
  name ends in `_test.kern` is a test file, left out of a module
  directory by every command except `kern test`; a named file is loaded
  whatever its name.
- **Paths.** Every path in this document is handled lexically: a
  directory or file reached through a symbolic link is read as the OS
  presents it, no link is resolved, and a path is spelled as it was
  given or composed. Two paths name the same file when their absolute
  forms, each resolved against the working directory and cleaned by
  `filepath.Clean`, are equal.
- **Import path.** The string of an `import "p"` declaration. A path that
  begins with `std/` names an embedded standard-library module and is
  outside this document. Any other path is a **user import path**: a
  relative path of one or more components separated by `/`, where no
  component is empty, `.` or `..`, and no component contains `\`, `:` or a
  control byte (below 0x20, or 0x7f). The **alias** of a path is its last component.
- **Project root.** As `spec/types.md` line 16 defines it (§4.1 restates
  it for completeness).
- **Manifest.** The file named `kern.mod` in the project root (§5).
- **Entry files.** The files an invocation starts from: the files named on
  the command line or in the request's `files`, or, when none is named,
  the manifest's `entry` (§4.2).
- **Discovered file set.** The entry files and, transitively, the files of
  every module a file of the set imports by a user import path (§4.3).

## 3. Non-goals

- No change to name visibility: imports stay non-transitive, exported
  names stay capitalised, the `std/` modules stay embedded.
- No package registry, dependency versioning, remote fetching, build
  scripts or several targets per manifest.
- No change to how the fixture harness in `tests/` runs fixtures (it
  names every file through the Go API; its coverage table may register
  a generated test for a code it cannot reach, as it does today), nor to
  the agent harness under `bench/agent/`, whose tools name the task's
  files.
- A module must not spread over several directories, and a directory
  must not hold several modules; discovery does not relax either rule.

## 4. Requirements

### 4.1 The project root

R1. The project root is determined once per invocation, before any file
is read as source, and does not change during the invocation.

R2. When at least one entry file is named, the root is the nearest
ancestor directory of the first named file (its own directory first, then
each parent up to the filesystem root) that contains a regular file named
`kern.mod`; when no such ancestor exists, the root is the working
directory of the `kern` process. This is `spec/types.md` line 16,
unchanged; a `kern.mod` that is found is the manifest, and a root reached
by the fallback has no manifest. An ancestor that cannot be inspected
(an OS error on reading it) is treated as not containing `kern.mod` and
the search continues upward.

R3. When no entry file is named, the root search starts at the working
directory and walks up the same way; the first directory containing
`kern.mod` is the root and that file is the manifest. When none exists,
the invocation fails with `PROJECT_NOT_FOUND` (§6).

R4. Named file paths are resolved relative to the working directory, as
today (`spec/types.md` line 16).

### 4.2 Entry files and the entry module

R5. The entry files are the files named on the command line or in the
request's `files`, in the order given, each with the name given; that
name is the file's name in every diagnostic, trap record and result, as
today.

R6. When none is named and the manifest has an `entry` (§5), the entry
files are the manifest's `entry` in its order, each resolved relative to
the project root and named by the path `<root>/<entry>` with `<root>`
spelled as R3 found it. When none is named and the manifest has no
`entry`, the invocation fails with `PROJECT_INVALID` (§6).

R7. The entry files are the whole entry set: on the entry files' own
account discovery does not add the other `.kern` files of an entry
file's directory. A module that spans several files is named file by
file, or listed in the manifest's `entry`. Rationale: directories such as
`tests/run/` hold many one-file programs of different modules, each run
by name, and completing the directory would turn every such run into
`MODULE_MISMATCH`. This is today's behaviour: a directory holds one
module (`spec/types.md`), and the files of that module that take part in
an invocation are the ones the invocation names; discovery adds the
rule's consequence for imports only. **OPEN DECISION (owner):**
`spec/types.md` does not say which files of a named file's directory
take part in an invocation; R7 is the default this document implements,
and the proposed amendment to `spec/types.md` line 16, to be made by the
owner, is the sentence "An invocation compiles the files it names and the
files that discovery adds for the imports among them; the other `.kern`
files of a named file's directory do not take part." The alternative,
completing every entry file's directory, is rejected for the rationale
above and is not implemented. When some file of the set imports a
path whose module directory is an entry file's directory, R9 applies to that import as to
any other: the directory's files are added, and R8 keeps the named file
under its given name.

R8. Every named occurrence is preserved: a file named twice (the same
file under §2's rule) is read twice, as today, and the resolver reports
the duplicate declarations. Discovery adds no occurrence of a file that
is the same file as any named file or any already discovered file, so a
file that is both named once and imported is read once, under the name
given.

### 4.3 The discovered file set

R9. Discovery starts with the entry files and repeats until no file is
added: for every file in the set, in set order, and every user import
path `p` it declares, in declaration order, the module directory
`<root>/<p>` is located and each of its `.kern` files, in byte order of
their names, is added unless R8 excludes it. Discovered files are named
by the path `<root>/<p>/<name>` with `<root>` spelled as R2 or R3 found
it, so that a root reached from a relative name yields relative names and
the fallback root yields names relative to the working directory.

R10. Every file of a discovered module directory must declare the module
name equal to the alias of the import path that located it. A file
whose module name differs is reported with `MODULE_MISMATCH` (the
existing code; message "file declares module %q, expected %q") at the
file's `module` declaration, whether discovery loaded the file or it was
named and its directory was then reached through an import. (For a
program whose imports resolve, every such file declares the alias, so an
invocation that names every file reports what it reported before this
document; one whose import names a directory of another module reported
`NAME_UNRESOLVED` at each use and now reports the cause.)

R11. An import path that is not a user import path (§2), or whose module
directory does not exist, is not a directory, cannot be enumerated, or
holds no `.kern` file, or one of whose `.kern` files cannot be read, is
reported with `MODULE_NOT_FOUND` at the span of the import declaration in
the importing file, the OS error quoted in the message when there is
one; a module directory that fails is reported once per import
declaration that names it, in each importing file, with no caching
across files. A named file that cannot be read is reported as today
(`API_FILE_NOT_FOUND`), before discovery. Precedence: the manifest is
validated first and its failure ends the invocation; then the named
files are loaded and a load failure ends it; discovery runs only when
both succeeded. Discovery continues with the remaining imports and
files so that every missing module is reported in one run, and the
invocation fails after discovery completes, before resolution.

R12. A cycle of imports (A imports B, B imports A) adds no file beyond its
first visit, since each module directory is visited at most once; the
resolver's existing handling of import cycles then applies unchanged.

R13. Discovery reads each file's complete contents (which it returns for
the later stages) but parses only the prologue, the `module` line and
the `import` declarations; a file whose prologue does not parse is
reported with the `SYNTAX` diagnostic the full parse would report for it
and contributes no imports (guidance: the existing parser, applied to
the prologue); discovery still completes for the other files
and the invocation fails after it, as under R11.

R14. The resolver, checker and every later stage receive the discovered
file set in this order: the entry files in the order given, then the
discovered files in the order R9 added them. The order is a function of
the entry list, the manifest and the directory contents alone.

R15. For an invocation that names every file of a program and whose
import paths resolve under the root to those same files (the same files
under §2's rule, whatever their spelling), discovery adds nothing. For
such an invocation with no manifest on the root walk, every command
behaves as before this document; with a manifest, the manifest's settings
and validation (§5) apply in addition, and nothing else changes. The fixture
suites under `tests/` name every file through the Go API and do not pass
through discovery; they are unaffected.

### 4.4 Commands

R16. `kern run [flags] [files...] [-- args...]`, `kern build [flags] [-o
out] [files...]` and `kern check [flags] [files...]` accept zero files when
R3 finds a manifest with an `entry`; with zero files and no manifest they
fail with `PROJECT_NOT_FOUND`, and with a manifest without `entry` with
`PROJECT_INVALID`.

R17. Every §7 machine command except `format` (`check`, `emit-ir`,
`explain-ownership`, `show-copies`, `show-allocations`,
`show-bounds-checks`, `show-layout`, `show-effects`,
`apply-structured-edit`) applies discovery to the request's `files`;
`format` stops after the entry files (R19). A
request with an empty `files` uses the manifest's `entry` under R3 and
R6; it fails with the existing `API_NO_FILES` when R3 finds no manifest
(an API client that names no files and has no project is told so, as
before this document), and with `PROJECT_INVALID` when the manifest has
no `entry`. `PROJECT_NOT_FOUND` is a command-line code (R16). A machine
command given no file and no request on standard input (end of file
before any JSON) is a request with an empty `files`.

R18. A result that lists the files of the program (`check`'s
`result.files`) lists the discovered file set in R14's order. A request
`span` must name a file of the discovered set by its R5, R6 or R9 name;
otherwise `API_INVALID_SPAN`, as today for a file outside the request.

R19. `format` rewrites only the files named in the request (or the
manifest's `entry` files) and runs no discovery: formatting is per file,
needs no other file, and must keep working while an import is missing
or under repair; the root and manifest are still determined (R1–R3, R6).

R20. The incremental check mode (`--cache`) receives the discovered file
set as its file list and is otherwise unchanged.

## 5. The manifest

R21. The manifest `kern.mod` is a text file that is either empty or holds
one JSON object with the fields below. Every field is optional, an
unknown field is rejected, and a value of the wrong type or outside its
set is rejected, each with `PROJECT_INVALID` whose message names the
field; when a field occurs more than once, the last occurrence is the
field's value and the only one validated, as Go's JSON decoder reads it. An empty file (zero bytes, or only JSON whitespace) is a valid
manifest with every default: it marks the project root and nothing else.

| field | type | meaning | default |
|---|---|---|---|
| `schema_version` | string | must be a schema version the compiler accepts (`diag.Accepted`: 0.2 and 0.3 at the time of writing) when present | the newest |
| `entry` | string, or array of one or more strings | the entry file(s), each a relative path with no `..` component, resolved under the project root | none |
| `opt` | string | `"debug"` or `"release"` | `"debug"` |
| `env` | string | `"hosted"` or `"freestanding"` | `"hosted"` |
| `target` | string | the LLVM target triple for `build` and the layout commands | the host triple |
| `output` | string | the executable `build` writes, a relative path resolved under the project root | none |

R22. A manifest that cannot be read is rejected with `PROJECT_INVALID`
quoting the OS error; one that is not valid JSON or has trailing content
after the object, with `PROJECT_INVALID` quoting the parse error; and one
whose JSON value is not an object, with `PROJECT_INVALID` and the message
"kern.mod must hold a JSON object".

R23. A setting given on the command line (`--opt`, `--env`, `--target`,
`-o`) overrides the manifest's value of the same setting; a request's
`target` overrides the manifest's `target`; the existing
`API_TARGET_CONFLICT` rule between a CLI `--target` and a request
`target` is unchanged. A manifest that R2 finds by walking up from a named
file supplies its settings to that invocation as well.

R24. `kern build` with neither `-o` nor a manifest `output` fails with
`CLI_INVALID_FLAGS`, as today.

## 6. Diagnostics

| code | rule | category | when |
|---|---|---|---|
| `MODULE_NOT_FOUND` | T.1 | lang | R11: an import path that is malformed, or names no enumerable module directory holding a `.kern` file; span: the import declaration; `related`: none; `fix`: none |
| `MODULE_MISMATCH` | T.1 | lang | R10, in addition to its existing use among named files: a discovered file declares a module other than the import path's alias |
| `PROJECT_NOT_FOUND` | P.1 | cli | R3, R16: `run` or `build` with no file named and no `kern.mod` in the working directory or its ancestors (a machine command answers `API_NO_FILES`, R17) |
| `PROJECT_INVALID` | P.2 | cli | R6, R21, R22: the manifest cannot be read as specified, or has no `entry` when one is needed; the message names the field or quotes the parse error |

R25. The new codes are registered in the diagnostic registry and the
rules lock (`compiler/diag/codes.go` and `spec/rules.lock` today). `MODULE_NOT_FOUND` is covered by a generated
reject/accept pair in the `tests` package (a temporary program importing
a missing module, then the same program with the module present),
registered in the fixture harness's coverage table as
`TYPE_TOO_MANY_VARIANTS` is, since the harness names every file and
cannot reach the code through a committed fixture; a CLI contract test
exercises the code through `kern check` as well. The two `cli` codes have
contract tests in `cmd/kern/cli_test.go`.

R26. Every diagnostic from discovery and the manifest is delivered as
today: in the single JSON response on standard output with exit status 2
for the machine commands, `run` and `build`, and in the human form under
`--human`.

R27. The usage line printed for a bad invocation, the README's running
and building sections and `spec/types.md`'s import bullets (the
reference that holds name resolution; `docs/ownership.md` declares it a
non-goal) each describe discovery and the manifest in one paragraph.

## 7. Implementation constraints

R28. Discovery and the manifest must be exposed as a Go API usable by
the fixture harness and other tools: a function taking the working
directory and the named files and returning the project root, the manifest's settings
(each with a flag saying whether the manifest set it), the discovered
file set in R14's order with each file's name and contents, and a list of
`diag.Diagnostic` for every R3, R6, R10, R11, R13, R21 and R22 failure;
an empty list means the set is complete. The CLI's observable behaviour
is §4–§6. (Implementation guidance, not a requirement: the API lives in
one package under `compiler/` that does not import `cmd/kern`, and
`cmd/kern` calls it once per invocation before any other source loading.)

R29. Discovery performs no network access. It reads only the named
files, the `kern.mod` candidates of the ancestor walk, and files and
directories lexically under the project root (§2: a link under the root
is followed as the OS presents it, and counts as under the root).

## 8. Acceptance criteria

A1. From the directory `tests/positive/import_call`, `kern run app.kern`
prints `42`, `9`, `drop 21` (the fixture's expected output) with only the
entry file named, and `kern build -o prog app.kern` builds an executable
that prints the same; the same holds for `import_iface`,
`import_funcval`, `import_const_array` and `tests/trap/import_trap`
(whose trap record names `lib/lib.kern`).

A2. With an empty `kern.mod` in that directory, `kern run
tests/positive/import_call/app.kern` from the repository root (root found
by walking up from the named file) prints the same output; with `kern.mod` holding `{"entry": "app.kern"}`,
`kern run`, `kern build -o prog` and `kern check` from that directory
succeed with no file named, and `kern check` from its `lib`
subdirectory finds the manifest by walking up.

A3. Each of these manifests yields `PROJECT_INVALID` with exit status 2
and one JSON response whose message contains the field's name (`optx`,
`opt`, `opt`, `entry`, `entry`, `schema_version`) for the first six
cases and the parser's error text for the last two: `{"optx": 1}`
(unknown field), `{"opt": 1}` (wrong type), `{"opt": "fast"}` (outside
the set), `{"entry": "../x.kern"}` (`..` component), `{"entry": []}`
(empty array), `{"schema_version": "0.1"}`, `{"entry": "a.kern"} x`
(trailing content), `{` (invalid JSON). `kern run` with no file in a
directory with no `kern.mod` in it or any ancestor yields
`PROJECT_NOT_FOUND`.

A4. A program with `import "nowhere"` yields `MODULE_NOT_FOUND` at the
import's span; a program importing a directory whose files declare the
wrong module yields `MODULE_MISMATCH` at the file's `module` line; a
program with two missing imports reports both; and a program whose
imports all resolve, every discovered file declaring the import alias as
its module, reports the same diagnostics, in the same order, as an
invocation that names every one of its files in R14's order and spelling.

A5. `go test ./...` passes with no fixture changed, and a contract test
shows `--opt`, `--env` and `--target` overriding manifest values.

A6. A trap record and every machine-API location name discovered files
by their R9 names: for `kern run app.kern` from the fixture directory,
`lib/lib.kern`.

A7. With the imported module present, `kern format` on a request naming
`app.kern` alone returns one formatted file while `kern check` on the
same request lists both files in `result.files`; with the imported
module removed, `kern format` still returns one formatted file while
`kern check` reports `MODULE_NOT_FOUND`.
