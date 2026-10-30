# Plan: `kern lsp`, a language server over the machine API

**Spec:** `specs/language-server/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec): four rounds,
all EXECUTABLE_WITH_CLARIFICATIONS, scores 72, 72, 72 and 79, no
critical finding; the last has every spec requirement covered. Folded
in: batch membership as the queue's content, the reader's shutdown,
decoder-based character boundaries, R9's operation order, canonical
round-trips, hover's resolver hook and panic answer, transport errors,
the log writer, and the framing and lifecycle test cases. The
remaining warning, the queue's worst-case retained memory, is accepted
under Risks.
**Order:** five phases, each ending with `golangci-lint run ./...` and
`go test ./...` green. Phase 1 changes existing packages without
changing behavior; Phases 2 to 4 build the new package
`compiler/lsp`; Phase 5 adds the command, the end-to-end test, the
documents and the measurements. The change lands as one commit after
Phase 5 unless the owner asks for more.

**Decisions that hold for every phase.**

- No new module dependency. The server implements the JSON-RPC
  framing and the subset of LSP 3.17 types it uses in `compiler/lsp`
  with `encoding/json`; `go.mod` keeps its one requirement.
- The server is single-threaded over its state: one reader goroutine
  decodes framed messages into a buffered channel (capacity 64, the
  spec's queue), sending each message the moment its body is read; a
  batch is the channel's content (spec glossary), and a message the
  reader holds while the channel is full is in a later batch. The
  memory the queue retains is at most 64 messages of at most 64 MiB
  each; real clients send far smaller ones, so no byte budget is kept.
  The caller owns the input: `Run` returns without waiting for the
  reader, whose every send selects on a `done` channel that `Run`
  closes, so a reader blocked on a full channel ends at once and one
  blocked in `Read` ends when the caller closes the input or the
  process exits; and
  one loop goroutine handles them in order, writes every response and
  notification, and owns all state. A batch (spec glossary) is what is
  in the channel when the loop finishes a message: the loop handles
  the next message when one is waiting (non-blocking receive) and runs
  the batch-end checks (R1) only when none is. Output goes through one
  writer that frames each message whole.
- Tests drive the server in-process through `lsp.New(in io.Reader, out
  io.Writer, opts Options)` and `(*Server).Run() int` (the exit
  status). `Options` carries test hooks: `OnCheck func(uri
  string)`, called once per program check, and `Check func([]*source.File)
  ([]diag.Diagnostic, error)`, which replaces the resolve-and-check
  call so that a test can inject a panic (R5); `Resolve
  func([]*source.File) *resolve.Program` likewise replaces the
  resolution hover runs, and defaults to `resolve.Resolve`. `Log
  io.Writer` (default `os.Stderr`) receives every log line, one per
  event, as `kern lsp: <operation> method=<m> id=<id> uri=<u>: <error>`
  with the fields that apply; errors are wrapped with the operation
  (`read`, `write`, `check`, `request`) and the client sees only the
  error text R5 defines. A fourth, `Start <-chan
  struct{}`, holds the loop (not the reader) until it is closed; with
  `(*Server).Queued() int`, the number of messages in the channel, a
  test that needs a batch (R1) writes every message into the input,
  waits until `Queued` reaches their count, and then closes `Start`,
  so the batch is exactly those messages. `Start` nil starts the loop
  at once.
- Paths: a document's path is the cleaned absolute path of its `file`
  URI, decoded with `net/url`; every file of a program is named by its
  absolute path (spec §2). Overlay keys are those paths.
- Exit statuses follow spec §3.5; `kern lsp` returns `Run`'s status.

## Phase 1: overlay in discovery, explain-ownership as a library (R10)

**Files.** `compiler/project/discover.go`, `compiler/project/project_test.go`,
`compiler/analysis/ownership.go` (new), `cmd/kern/explain.go`.

**Steps.**

1. `project`: add `type Options struct { Discover, Tests bool; Overlay
   func(abs string) ([]byte, bool) }` and `func LoadWith(cwd string,
   named []string, o Options) Result`. `Load`, `LoadTests` and
   `Entries` become calls of `LoadWith` with the options they imply
   (`Overlay` nil), so their behavior is unchanged. Every file read in
   `load` and `entryTests` (`source.Load`) goes through one helper
   `readFile(cwd, name, o)`, which asks `Overlay` with `identity(cwd,
   name)` first and reads disk otherwise. The manifest and directory
   listings stay disk reads (spec R1). A named file that exists only in
   the overlay loads; its directory's listing does not add it again
   (it is in `seen`).
2. `analysis`: move the body of `explainOwnership` after resolution,
   checking, lowering and validation into `func ExplainOwnership(prog
   *resolve.Program, mod *ir.Module, file string, span diag.Span)
   diag.ExplainOwnershipResult`, unchanged in logic and order.
   `cmd/kern/explain.go` keeps its argument validation, resolution,
   checking, lowering and validation and calls the new function, so
   its responses are byte-identical (R10).
3. Tests: `TestLoadOverlay` in `project_test.go`: an overlay text
   replaces an entry file's disk text, replaces a discovered module
   file's disk text, and loads a named file absent from disk; a nil
   overlay matches `Load`.

**Acceptance.** The existing `cmd/kern` explain-ownership tests and
the harness pass unchanged; `TestLoadOverlay` passes.

## Phase 2: text positions and minimal edits (R8, R9)

**Files.** `compiler/lsp/text.go`, `compiler/lsp/text_test.go`,
`compiler/lsp/uri.go`, `compiler/lsp/uri_test.go`.

**Steps.**

1. The three protocol types these functions use, with their LSP JSON
   field names: `Position{Line, Character int}` (`line`,
   `character`), `Range{Start, End Position}` and `TextEdit{Range
   Range; NewText string}` (`range`, `newText`), in `text.go`; Phase 3
   adds the rest to `protocol.go`. `type Encoding int` with `UTF8` and
   `UTF16`. `func ToPosition(text
   []byte, off int, enc Encoding) Position` and `func ToOffset(text
   []byte, p Position, enc Encoding) int`, implementing R8 exactly:
   line breaks `\n`, `\r\n`, lone `\r`; an offset is first clamped to
   `[0, len]`, moved back to a character boundary and, when it is between
   `\r` and `\n`, to the `\r`; a character beyond the line's end is
   the line's end; a line beyond the last is the end of the text; a
   character inside a multi-unit character is its start. Invalid UTF-8
   bytes (`utf8.DecodeRune` returning `RuneError` with size 1) count
   one unit in both encodings. Runes above U+FFFF count 2 UTF-16 units.
   A character boundary is an offset where a step of decoding the text
   from its start with `utf8.DecodeRune` begins, or the end; so an
   isolated continuation byte or a truncated sequence is a one-byte
   character with boundaries on both sides. One helper,
   `boundaries(text)`, defines it for both functions.
2. `func MinimalEdit(old, new []byte, enc Encoding) (TextEdit, bool)`
   implementing R9 in its order: the common prefix, shortened while its
   end is not a character boundary of both texts (step 1's
   `boundaries`); then the common suffix of the remainders after it,
   shortened likewise at its start, so the two never overlap; then a
   prefix ending, or a suffix starting, between the `\r` and `\n` of a
   `\r\n` of `old` is shortened by one byte. The range is converted
   with `ToPosition` on `old`. Returns false when equal. Tests assert
   the exact range and replacement for inserting and deleting a `\r\n`
   and for an edit that starts inside one.
3. `uri.go`: `func PathOf(uri string) (string, bool)` (only `file`
   URIs; percent-decoded; cleaned; must be absolute) and `func URIOf(path
   string) string` (`url.URL{Scheme: "file", Path: path}`).
4. Tests: the table of spec §5 item 1, plus round-trips
   `ToOffset(ToPosition(off)) == off` for every canonical offset of a
   mixed text in both encodings (a character boundary not between `\r`
   and `\n`), and for every other offset `ToOffset(ToPosition(off))`
   equals its normalization (the boundary at or before it, then the
   `\r` for the `\r\n` interior); `MinimalEdit` applied to `old`
   reproduces `new` for every case, including edits next to an
   isolated continuation byte and a truncated sequence.

**Acceptance.** `go test ./compiler/lsp` passes.

## Phase 3: protocol, documents, checks and publication (§3, R1–R5)

**Files.** `compiler/lsp/jsonrpc.go`, `compiler/lsp/protocol.go`,
`compiler/lsp/server.go`, `compiler/lsp/check.go`,
`compiler/lsp/server_test.go`.

**Steps.**

1. `jsonrpc.go`: `readMessage(*bufio.Reader) ([]byte, error)` parses
   the header block (case-insensitive names, `Content-Length` required,
   at most 64 MiB, `Content-Type` accepted and ignored) and reads the
   body; a header error or end of input is a fatal error (spec §3.2).
   `writeMessage(w, v)` marshals and frames one message under a mutex
   and returns any write error (a short write is an error, as
   `io.Writer` defines). A write error ends `Run` with status 1 after
   one line on standard error naming the method or request id; `Run`
   closes `done` on every return. Log lines go to standard error only.
2. `protocol.go`: the request, response, error and notification
   envelopes; `Position`, `Range`, `Location`, `TextEdit`,
   `Diagnostic`, `DiagnosticRelatedInformation`, `PublishDiagnosticsParams`,
   the `initialize` params fields the server reads
   (`capabilities.general.positionEncodings`,
   `capabilities.textDocument.codeAction.codeActionLiteralSupport`)
   and the result, the document notifications' params, `Hover`,
   `MarkupContent`, `CodeAction`, `WorkspaceEdit`. Error codes
   `-32700`, `-32600`, `-32601`, `-32602`, `-32603`, `-32002` as
   constants.
3. `server.go`: lifecycle states (before `initialize`, running, after
   `shutdown`) with the answers of spec §3.1; dispatch by method;
   `didOpen`, `didChange` (full and ranged changes; a reversed range
   ignores the notification and logs type 2; a change to a URI that is
   not a document is ignored), `didClose`, `didSave` (ignored); a document
   table keyed by URI holding text, version, path, and an open/change
   sequence number for the alias rule; every document event marks all
   kern documents stale (R1). A body that is not a JSON object answers
   `-32700` with `id: null`; an object with an `id` and no string
   `method` answers `-32600`; a message with neither is ignored.
   Request handlers run under `recover`; a panic answers `-32603`
   (R5).
4. `check.go`: `program(doc) (srcs []*source.File, ds
   []diag.Diagnostic)` builds spec §2's entry set (siblings by
   `os.ReadDir` of the document's directory, prologues parsed from
   overlay-or-disk text with `parser.ParsePrologue`), loads it with
   `project.LoadWith(dir, entries, Options{Discover: true, Tests: isTest
   && prologueOK, Overlay: s.overlay})` with `dir` as the working
   directory, and makes every name absolute. `checkDoc(doc)` runs the
   loader's diagnostics or `Options.Check` (default: `resolve.Resolve`
   then `check.Check`, panics recovered into an error), applies R2's
   relocation, then publishes per R2/R3. `checkDoc` runs whole under
   `recover`, program construction and prologue parsing included, and
   a non-panic error the `Check` hook returns is treated as a panic's
   text (R5); a request handler's returned error answers `-32603`.
   After that it publishes per R2/R3 and records, per document, the
   set of URIs it published and, per URI, the diagnostics last
   published and the documents whose check published it (for R4 and
   R7). An internal error publishes the `INTERNAL` diagnostic on *D*
   and sends `window/logMessage` type 1 (R5). The batch end checks
   the stale kern documents in URI order (R1); `didClose` publishes
   empty arrays per R4 before the batch end re-checks the others.
5. Tests (in-process): initialize result and encoding pick both ways;
   request before initialize (`-32002`); second initialize and request
   after shutdown (`-32600`); unknown request (`-32601`) and unknown
   notification (no answer); notifications before `initialize` and
   after `shutdown` ignored; a body that is not JSON and one that is a
   JSON array (`-32700`, then the server still answers); header names
   in mixed case; a missing `Content-Length`, one above 64 MiB and end
   of input inside a body each end with status 1; a writer that fails
   ends `Run` with status 1; `exit` after
   `shutdown` status 0, without it 1, end of input after shutdown 0;
   `Run` returning while the reader is blocked on a full channel and on
   an open pipe (the test closes the pipe afterwards and the goroutine
   count returns to its start);
   three queued `didChange` give one `OnCheck` call (R1) and the
   published diagnostics match the last text; an error removed is
   republished empty; R2's relocation of a file-less diagnostic; R4's
   empty publication on close; R5 with a panicking `Check` hook, the
   server answering the next request.

**Acceptance.** `go test ./compiler/lsp` passes, race detector on
(`go test -race ./compiler/lsp`).

## Phase 4: formatting, hover and quick fixes (§3.4, R6, R7)

**Files.** `compiler/lsp/features.go`, `compiler/lsp/features_test.go`.

**Steps.**

Every handler first resolves its URI: formatting needs a document
(else `-32602`); hover and code actions need a kern document and
answer `null` and `[]` otherwise (spec §3.4). Every handler runs under
the request `recover` of Phase 3, which covers program loading as well
as the compiler, so a panic anywhere answers `-32603` (R5).

1. Formatting: `format.Format` on the document's text, the request's
   options ignored; `null` on parse
   diagnostics; `[]` when unchanged; else the one `MinimalEdit`.
   Non-document URI: `-32602`.
2. Hover: build the program (Phase 3 step 4) and resolve it with the
   `Resolve` hook (default `resolve.Resolve`) inside the request
   `recover`; find *D*'s `ast.File` among `prog.Modules` by name;
   `ast.Walk` it for the innermost `*ast.Ident` with `Start <= off <=
   End` (ties: the first found); the symbol is `prog.Info.Idents[id]`,
   or the `prog.Info.Decls` symbol whose `Pos` equals the ident's span
   and whose file is *D*. The code block per R6 uses `Symbol.Type`,
   `Symbol.Conv` and, for functions, `FuncDecl.Module`, `Name` and
   `Type`. When the loader, the resolver and `check.Check` together
   report no diagnostic, lower and validate as
   `explain-ownership` does and call `analysis.ExplainOwnership` with
   the declaration's span, then render the place per R6 with 1-based
   lines from the place's file text.
3. Code actions: refresh *D* if stale (R7), then for each diagnostic
   last published on *D*'s URI with a fix and a range intersecting the
   request's: `edit.Apply` on the program's files, one `MinimalEdit`
   per changed file whose text changed, keyed by R2's URI choice; each
   action has title `Apply the compiler's fix for <code>`, kind
   `quickfix` and the published LSP diagnostic, in published order;
   nothing when the applier fails or nothing changed; `[]` when the
   client lacks `codeActionLiteralSupport`.
4. Tests: formatting on unformatted, formatted and unparsable text;
   hover on a local's declaration and use, a parameter with a
   convention, a function, a type name, and whitespace (`null`), each
   in a program with and without diagnostics (the ownership lines
   present only without); a quick fix for a checker fix (the missing
   `?` of `check/stmts.go`) and a resolver fix (an `own` insertion of
   `resolve/bodies.go`), each applied to the text and re-checked clean;
   a request range away from the diagnostic gives `[]`; a client
   without literal support gets `[]`; a `Resolve` hook that panics
   makes hover answer `-32603` and the next request is answered.

**Acceptance.** `go test -race ./compiler/lsp` passes.

## Phase 5: command, end-to-end test, documents, measurement (§1, R11, R12)

**Files.** `cmd/kern/main.go`, `cmd/kern/lsp_test.go` (new),
`docs/editors.md` (new), `README.md`,
`docs/reports/language-server.md` (new).

**Steps.**

1. `cmd/kern/main.go`: dispatch `lsp` before the machine commands;
   arguments are a usage error (one line on standard error, status 2);
   otherwise `lsp.New(os.Stdin, os.Stdout, lsp.Options{}).Run()`. Add
   `lsp` to the usage line.
2. `cmd/kern/lsp_test.go`: build the binary into the test's temporary
   directory (`go build`), write a project (`kern.mod` with an entry,
   `main.kern` importing `lib`, `lib/lib.kern` with one type error),
   start `kern lsp`, initialize, open `main.kern` with unsaved text
   that differs from disk, and assert: a diagnostic published on
   `lib/lib.kern`'s URI; the diagnostics of `main.kern` follow the
   unsaved text; `shutdown` then `exit` gives status 0.
3. `docs/editors.md`: the command; Neovim 0.11 (`vim.filetype.add`
   for `kern`, `vim.lsp.config` and `vim.lsp.enable`), Helix
   (`languages.toml` language and server entries), Emacs eglot
   (`define-derived-mode` and `eglot-server-programs`); what the server
   provides and the spec's non-goals. `README.md`: one line in the
   commands list and a link.
4. Manual Neovim check (spec §5 item 5): `nvim --headless` with a
   scratch init file that configures the server, opens an example with
   a deliberate error, waits for diagnostics, prints them, formats the
   buffer, and exits; the commands and outcome go in the report.
5. R12: time from writing a `didChange` to reading its
   `publishDiagnostics` for the repository's largest multi-module
   example (chosen by file count under `examples/`), median of 20, with
   the time of `kern check` on the same files beside it; recorded in
   the report with the host load, no gate.
6. The report `docs/reports/language-server.md`: what was built, the
   measurements, the Neovim session, what is not done.

**Acceptance.** `golangci-lint run ./...` and `go test ./...` green;
the Neovim session shows the diagnostics and the formatting edit; the
report exists.

## Risks

- **Retained input.** The queue may hold 64 messages of up to 64 MiB,
  4 GiB in the worst case. The client is the user's own editor on the
  same machine, whose messages are a few kilobytes; a byte budget would
  add a second blocking condition to the reader for no observed case,
  so the bound is documented in `docs/editors.md` and not enforced
  further.

- **Hover on a program with errors.** The resolver may record partial
  information after a diagnostic; hover uses whatever symbol it
  recorded and never lowers such a program. If resolution panics on
  broken input, the request answers `-32603` (R5) and the server
  continues; a test injects such a panic through the `Resolve` hook
  and sends a second request.
- **Alias rule cost.** Two URIs for one path are rare; the rule is
  implemented by choosing, per path, the document with the highest
  sequence number, a scan of the document table per read.
- **Checks on every keystroke batch.** Each batch end re-checks every
  open kern document. With many open documents in one project this
  repeats work; R12 measures one document, and the report states the
  per-document cost so the owner can judge whether the module cache
  (a non-goal here) is needed next.
