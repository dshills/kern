# Module discovery and the project manifest

**Date:** 2026-10-29. **Spec:** `specs/module-discovery/SPEC.md` v0.7
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/module-discovery/PLAN.md` (plancritic EXECUTABLE_WITH_CLARIFICATIONS,
no critical finding, no open question). **Scope:** the command line and
the machine API; the language is unchanged.

## What changed

- `compiler/project` (new): `FindRoot` walks from the first named file's
  directory, or the working directory, up to a `kern.mod`; `Load` reads
  the manifest, the entry files (named, or the manifest's `entry`) and,
  breadth-first through their imports, the `.kern` files of every imported
  module directory under the root, with `MODULE_NOT_FOUND` at each import
  that names no module and `MODULE_MISMATCH` for a file that does not
  declare the alias; `Entries` stops after the entry files, for `format`.
  `parser.ParsePrologue` reads a file's `module` line and imports without
  parsing its body.
- `kern run`, `kern build` and every machine command call it once before
  any other loading. `run` and `build` accept no file when the manifest
  has an `entry`; `build` takes `-o` from the manifest's `output`; `--opt`,
  `--env` and `--target` override the manifest. A machine command with no
  file and no request on standard input is a request with an empty
  `files`; with an empty `files` and no manifest it answers `API_NO_FILES`
  as before.
- Three codes: `MODULE_NOT_FOUND` (T.1, lang, covered by a generated
  reject/accept pair in the `tests` package since the fixture harness
  names every file), `PROJECT_NOT_FOUND` (P.1, cli) and `PROJECT_INVALID`
  (P.2, cli), with contract tests in `cmd/kern/project_test.go`.

## Behaviour worth knowing

- **The fallback root is the working directory**, as `spec/types.md`
  line 16 prescribed before this work. `kern run tests/positive/import_call/app.kern`
  from the repository root therefore reports `MODULE_NOT_FOUND` for
  `import "lib"` (there is no `lib/` at the repository root), while the
  same command from the fixture's directory, or with an empty `kern.mod`
  in it, runs the program. A `kern.mod` is the way to anchor a project
  that is run from elsewhere.
- **The entry module is exactly the named files.** The other `.kern`
  files of an entry file's directory do not take part unless an import
  reaches that directory. The alternative, completing the entry
  directory, would turn `kern run tests/run/x.kern` into
  `MODULE_MISMATCH`, since such directories hold many one-file programs.
  `spec/types.md` does not say which files of a directory take part in an
  invocation; the spec records this as the owner's open decision with a
  proposed sentence (SPEC.md R7).
- **Discovered files are named relative to the working directory** when
  the entry file is, so trap records and machine-API locations say
  `lib/lib.kern`, and absolute when the entry file is absolute.
- Formatting runs no discovery: it works while an import is missing.

## Acceptance evidence

`cmd/kern/project_test.go`: every `import_*` fixture runs, checks and
builds with only `app.kern` named, the trap fixture's record names
`lib/lib.kern` (A1, A6); an empty `kern.mod` anchors a run from the
parent directory, a manifest with `entry` serves `run`, `check`, `format`
and `build` with no file, from the root and from a subdirectory (A2, A7);
eight invalid manifests yield `PROJECT_INVALID` naming the field or
quoting the parser, with `--human` rendering (A3); two missing imports are
both reported, a wrong module name is `MODULE_MISMATCH`, and the
diagnostics of an imported module's errors are byte-identical between
naming the entry and naming every file (A4); `--opt`, `--env` and
`--target` override the manifest and `API_TARGET_CONFLICT` still holds
(A5). `compiler/project/project_test.go` covers the root walk, the
manifest grammar, import-path validity, ordering, deduplication, cycles,
an entry directory reached by an import, unreadable files and absolute
entries. `go test ./...` passes with no fixture changed.

## Reviews

speccritic took the spec through seven rounds (two contradictions with
`spec/types.md`'s project-root rule and the entry-module question were
the critical ones). plancritic's two blocking findings, whole-file
parsing against the prologue-only rule and a typed manifest decoder that
could not implement the duplicate-field and null rules, shaped
`ParsePrologue` and the raw-message decoder. prism (gpt-6.1-sol) found,
and this change fixed: `format` blocked by a missing import, a named file
in an imported directory escaping the module-name check, an absolute
manifest path joined to the working directory, and DEL accepted in an
import path.
