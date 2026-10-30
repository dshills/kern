# A language server over the machine API

**Date:** 2026-10-30. **Spec:** `specs/language-server/SPEC.md` v0.1.
**Plan:** `specs/language-server/PLAN.md`. **Item:** the tenth of the
ten-item list ("a language server over the machine API, `check`,
`format`, `explain-ownership` and the structured fixes, would give
humans what the structured arm gives agents").

## Before

Everything an editor needs was in the compiler: diagnostics with a code,
a rule and, since schema 0.3, a sentence; the canonical format;
machine-applicable fixes on several diagnostics; `explain-ownership`.
None of it reached an editor. A person editing kern saw nothing until
they ran `kern check` and read JSON.

## What changed

- **`kern lsp`.** A driver command that speaks the Language Server
  Protocol 3.17 over standard input and output, implemented in the new
  package `compiler/lsp` with no new dependency. It provides four
  things:
  - Diagnostics as you type, for every open `.kern` file. Each file is
    checked as `kern check` would check it, with the editor's unsaved
    text. A diagnostic in an imported module appears on that module's
    file.
  - Formatting, as one minimal edit to the canonical form.
  - The compiler's fixes as quick-fix code actions.
  - Hover. A local, parameter or binding shows its type and convention,
    and, when the program checks cleanly, the explain-ownership facts:
    its state, its accesses by line, its drop point and the contract.
    A function shows its type.
- **The program of a document.** It is the document, the other files of
  its module in its directory, and everything they import, found from
  `kern.mod` as on the command line. A test file brings in its module's
  test files, as under `kern test`. Module discovery gained an overlay
  so that unsaved buffers replace disk contents in every file read
  (`project.LoadWith`).
- **Batching.** Every edit marks every open `.kern` file stale. The
  server checks the stale files once it has handled every message it
  has queued, so a burst of keystrokes costs one check per file.
- **Positions.** The server uses UTF-8 when the editor offers it and
  UTF-16 otherwise. It handles `\r\n` and lone `\r` line ends and
  invalid UTF-8.
- **`docs/editors.md`.** Setup for Neovim 0.11+, Helix and Emacs
  (eglot), and what the server does not do. The README lists the
  command.

## A bug found on the way: explain-ownership read every function

Hover is built on `explain-ownership`, and the first hover showed wrong
facts. The command matched a place's symbol to IR locals by name in
every function of the module, library code included. In this program,
the local `v` of `main` reported the view that `total` takes of its own
parameter `v`, and the contract of a `Vec` method:

```kern
func total(v: own Vec[u64]) -> u64 { return v.get(0usize) }
func main() -> i32 {
    v := Vec[u64].new()
    v.push(1u64)
    n := total(move v)
    ...
```

A place now reads only its declaring function: the narrowest
non-library function of its file whose span holds the declaration, with
every instantiation of a generic one. This is spec R13. The set of
places is unchanged, so the benchmark harness's place counts do not
move. A new CLI test pins the case above. The explain-ownership logic
moved from the driver into `analysis.ExplainOwnership`, and the driver
calls it.

## Verification

- **Unit tests** cover position conversion in both encodings and minimal
  edits, including `\r\n` boundaries, invalid bytes, truncated
  sequences, and round trips at every offset.
- **Protocol tests** run the server in process. They cover the
  lifecycle and its error codes, malformed bodies, framing errors, a
  failing writer, batching (three queued changes give one check), and
  publication. Publication covers clearing, relocating a file-less
  diagnostic, an imported module's diagnostics and their clearing on
  close. They also cover a panicking check and a panicking resolver,
  hover, formatting, ranged changes, and the quick fixes. One quick fix
  comes from the resolver and one from the checker, and each fixed text
  re-checks clean. The tests pass under the race detector.
- **End to end.** A test builds the `kern` binary, runs `kern lsp` on a
  two-module project with a `kern.mod`, and sees two things: the
  imported module's diagnostic on its own file, and the unsaved
  buffer's error instead of the file on disk.
- **In a real editor.** Headless Neovim 0.12.6 ran the configuration
  from `docs/editors.md`, with `filetype on` added because the minimal
  init does not enable it. It opened a copy of `examples/testing/app.kern`
  that had a parameter missing `own` and an unformatted `return`:

  ```
  filetype: kern
  attached: true
  diagnostics arrived: true
    line 16 col 8 [PARAM_OWN_REQUIRED] parameter v has move-only type Vec[u64] and must be declared own (rule 5.2.1)
  client: kern, encoding utf-8
  hover: "```kern\nlocal s: Stack\n```\n"
  format changed the buffer: true; 'return   0i32' now gone
  code actions: { "Apply the compiler's fix for PARAM_OWN_REQUIRED" }
  after applying the fix, diagnostics cleared: true
  fixed line: func f(v: own Vec[u64]) { }
  ```

  The hover shows no ownership lines because the program had an error
  at that moment. That is the specified behavior.

## Cost (R12)

Time from writing a `didChange` to reading its diagnostics, median of
20, against `kern check` on the same file as a fresh process. The host
load average was about 17 throughout, from processes outside this work,
so these are upper bounds.

| Program | Server | `kern check` |
|---|---|---|
| `examples/testing` (the largest multi-module example, 2 files) | 1.8 ms | 6.1 ms |
| `bench/kern/buffer_pool/buffer_pool_fail.kern` (the largest program, 269 lines) | 2.9 ms | 6.6 ms |

The server pays only the check: the command's extra time is process
start and loading the prelude. Every open `.kern` file is re-checked on
each batch, so the cost grows with the number of open files.

## Reviews

- **speccritic:** VALID_WITH_GAPS with no critical finding after five
  rounds. The first was INVALID only on section names.
- **plancritic:** four rounds of EXECUTABLE_WITH_CLARIFICATIONS. The
  last scored 79 with every requirement covered.
- **prism:** five passes. The fixes it led to:
  - A binary test that cannot leave the server running or race on its
    stderr.
  - A file's diagnostics kept while another open document still
    publishes them.
  - Bounded header lines.
  - Malformed `initialize` parameters rejected.
  - Code actions that honor the request's kind filter.
  - The object form of `codeActionProvider` offered only to clients
    that support it.
  - JSON-RPC envelope checks, including `-32600` rather than `-32700`
    for valid JSON that is not a request object.
  - Freed publication records.
  - Bounded waits in the reader test.

  Three findings stayed open by decision:
  - **Unsaved siblings.** Siblings that exist only in the editor are
    left out of a program, because the spec reads directory listings
    from disk.
  - **Blocked reads.** A read blocked on an input the caller keeps open
    outlives `Run`. The caller owns the input, and `kern lsp` exits with
    the process.
  - **Windows URIs.** Windows drive-letter URIs are not handled,
    because kern does not run on Windows.
- **realitycheck:** PARTIALLY_ALIGNED, score 100, no violations. Its
  partial items are those it cannot judge from the code inventory
  alone.
- **verifier:** 16 recommendations, all from name matching on exported
  symbols. The tests above exercise each one (`New`, `Run`, `Queued`,
  `PathOf`, `URIOf`, `LoadWith`, `ExplainOwnership`).

## Not done

- **Not provided.** Completion, go-to-definition, references, rename,
  semantic highlighting, a syntax grammar and an editor extension
  package are not provided. The resolver's identifier table would make
  go-to-definition small.
- **Directory listings come from disk.** A new file that exists only in
  the editor is seen by other files' programs once it is saved.
- **Checks are full.** Each check re-resolves the whole program. The
  module cache behind `kern check --cache` is not used.
- **Windows.** Windows file URIs with drive letters are not handled,
  because kern's runtimes are POSIX only.
