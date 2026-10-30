# `kern lsp`: a language server over the machine API

**Version:** 0.1 (2026-10-30). **Scope:** a new driver command
`kern lsp` (`cmd/kern`), a new package that implements the server
(`compiler/lsp`), an overlay of unsaved editor buffers in module
discovery (`compiler/project`), the move of the explain-ownership
computation from the driver into a library function
(`compiler/analysis`, called by `cmd/kern`) with one correction to
it (R13), and the
documents (`README.md`, a new `docs/editors.md`). **Language impact:**
none. The machine-API commands and wire format of the language spec
§16 are unchanged: `kern lsp`, like `run`, `build` and `test`, is a
driver command, and it speaks the Language Server Protocol instead of
the §7 request and response. **Related:**
`specs/initial/AI_Native_Systems_Language_SPEC.md` §7 (the machine
API), §16 (diagnostics, structured edits, explain-ownership places);
`specs/module-discovery/SPEC.md` (the file set of a program);
`specs/diag-message/SPEC.md` (the diagnostic's sentence);
`specs/test-command/SPEC.md` §2.1 (test files in discovery);
`spec/format.md` (the canonical form).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `specs/module-discovery/SPEC.md` as context): the first draft was
INVALID only on the preflight's section names; its warnings (re-check
triggers that missed the manifest, an undefined batch, aliased paths,
overlapping prefix and suffix in a minimal edit, related locations)
were folded in. Four more VALID_WITH_GAPS rounds, no critical finding,
added the request order within a batch, sibling read failures,
`\r\n` boundaries, reversed change ranges, the test-file fallback,
fixes that change nothing, publication after relocation and the URI of
a fix's file. The remaining warnings are the preflight's flags on
acronyms in code identifiers and on the goal's prose.

**Glossary.** R*n*: requirement *n* of §4. LSP: the Language Server
Protocol, version 3.17
(<https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/>).
Client: the editor process that starts `kern lsp`. Document: a text
the client has opened with `textDocument/didOpen` and not closed.
Overlay: the current text of every document, which replaces the disk
contents of that file in everything the server reads. Kern document: a
document whose URI has the `file` scheme and whose path ends in
`.kern`. Program of a document: the file set §2 defines. Check: the
work of `kern check` on a file set, resolution and then type and
ownership checking, at opt level `debug`. Position encoding: the unit
the LSP `character` offset counts, negotiated at `initialize` (§3.1).
Fix: the structured edit (§16) a diagnostic carries in its `fix`
field. Span: a half-open byte range `[start, end)` of a file, as §16
defines it. Acronyms: API, application programming interface; JSON,
JavaScript Object Notation; JSON-RPC, the JSON remote procedure call
protocol LSP messages follow; URI, uniform resource identifier; UTF-8
and UTF-16, the Unicode transformation formats of 8- and 16-bit code
units; ASCII, the 7-bit American Standard Code for Information
Interchange; IR, the compiler's intermediate representation; README,
the repository's top-level `README.md`; MiB, 2^20 bytes; KiB, 2^10 bytes. Batch: the
messages in the server's input queue, read and not yet handled, at the
moment it finishes handling a message (§3.2); the queue holds at most
64 messages, and a message read while the queue is full joins it when
there is room, so it belongs to a later batch. A test may queue every
message of a batch before the server starts.

## 0. Goals and non-goals

A person editing kern in an editor gets what the structured arm of the
agent study gets from the machine API: the compiler's diagnostics as
they type, with the diagnostic's sentence; the canonical format on
request; the compiler's machine-applicable fixes as quick-fix code
actions; and, on hover, the ownership facts `explain-ownership` reports
(the binding's state, the accesses opened on it, where it is
dropped). One server works with any LSP client; the repository
documents the configuration of three editors. Non-goals: completion,
go-to-definition, references, rename, semantic highlighting, a syntax
grammar for any editor, an editor extension package, signature help,
workspace symbols, range formatting, pull diagnostics, incremental
re-checking through the module cache, and files that exist only in the
editor inside an imported module directory (§2).

## 1. Command

`kern lsp` takes no arguments and no flags. It reads LSP messages from
standard input and writes them to standard output; it writes free-form
log lines only to standard error. Any argument is a usage error: the
command prints one usage line to standard error and exits 2 without
reading standard input. The exit status is §3.5's.

## 2. The program of a document

The program of a kern document *D* at absolute path *P* is the file set
`kern check` loads when run in *P*'s directory with these entry files,
read through the overlay:

1. *P* itself, first.
2. Every other regular file directly in *P*'s directory whose name ends
   in `.kern`, that is not a test file (its base name does not end in
   `_test.kern`), and whose prologue parses and declares the same
   module as *D*'s prologue, in byte order of their names. When *D*'s
   prologue does not parse or the directory cannot be listed, the entry
   set is *P* alone; a sibling that cannot be read or whose prologue
   does not parse is left out, and the check reports nothing for it.
3. When *P* is a test file and *D*'s prologue parses, the set is loaded with the test-file rule
   of `kern test` (`specs/test-command/SPEC.md` §2.1), so the module's
   other test files join it; otherwise test files are excluded as in
   `kern check`.

Discovery then adds the files of every imported module, as
`specs/module-discovery/SPEC.md` defines, with the project root found
from *P* as `kern check` finds it. Every file the load reads, entry or
discovered, is read from the overlay when a document holds it (matched
by cleaned absolute path) and from disk otherwise. When two documents
with different URIs have the same cleaned absolute path, the one
opened or changed most recently supplies the text for every read of
that path, *P* and its prologue included, and R2 publishes that file
on its URI. Directory listings
are read from disk only: a document that exists only in the editor is
part of a program only as rule 1's *P*, or as rule 2's sibling when it
is also on disk.

Each file of the program has an absolute path: a name discovery
returns relative to *P*'s directory is joined to that directory.

## 3. Protocol

### 3.1 Lifecycle and capabilities

- The server answers `initialize` with these capabilities and no
  others: `positionEncoding` (below), `textDocumentSync` with
  `openClose: true` and `change: 1` (full text), `hoverProvider: true`,
  `documentFormattingProvider: true`, and `codeActionProvider`: an
  object with `codeActionKinds: ["quickfix"]` when the client declared
  `codeActionLiteralSupport`, and `true` otherwise, since LSP allows the
  object only for such clients. It reports `serverInfo` with name
  `kern` and, as its version, the machine API's `schema_version`
(`0.3`), since the driver has no version of its own.
- Position encoding: the server picks `utf-8` when the client lists it
  in `capabilities.general.positionEncodings`, and `utf-16` otherwise,
  and uses the pick for every position it reads and writes.
- An `initialize` whose `params` are present but are not an object of
  the LSP shape (a field of the wrong JSON type) gets error `-32602`
  and the server stays uninitialized; absent fields take the defaults.
- Before `initialize`, a request other than `initialize` gets error
  `-32002` (server not initialized) and a notification other than
  `exit` is ignored. A second `initialize` gets error `-32600`
  (invalid request).
- `shutdown` gets a `null` result. After it, every request gets error
  `-32600` and every notification but `exit` is ignored.
- An unknown request gets error `-32601` (method not found); an unknown
  notification, including `$/cancelRequest` and `$/setTrace`, is
  ignored. Every request is answered; a cancelled request is answered
  as if it had not been cancelled.

### 3.2 Framing

Each message is a header of `Content-Length: N` and optionally
`Content-Type`, each line ending in `\r\n`, an empty line, and *N*
bytes of UTF-8 JSON. Header names are case-insensitive. A header line
longer than 64 KiB or a header block of more than 64 lines is a
framing error. A body that is
not JSON gets error `-32700` (parse error) with `id: null`, and JSON
that is not an object (batches included) gets error `-32600` with
`id: null`; the server continues. An object whose `jsonrpc` is not
`"2.0"`, whose `id` is present but not a string, a number or `null`, or
that has neither an `id` nor a string `method` gets error `-32600` with
`id: null`; an object without a string `method` and with a valid `id`
gets error `-32600` with that `id` (the server sends no requests, so
no response is expected from the client). A header block without a valid
`Content-Length`, a `Content-Length` above 64 MiB, or end of input
inside a message ends the server as end of input does (§3.5). The
server writes messages in the same framing, one at a time, never
interleaved.

### 3.3 Documents

- `didOpen` stores the document's text and version. `didChange`
  replaces the text with each content change in order: a change
  without `range` is the whole new text; a change with `range`
  replaces that range, positions read in the negotiated encoding (R8);
  and it stores the version. When a change's range starts after its
  end, the whole notification is ignored, the text and version stay as
  they were, and the server sends a `window/logMessage` of type 2. `didClose` drops the document. `didSave` is
  ignored. A change to a URI that is not a document is ignored.
- Every document of any URI scheme and path is held in the overlay;
  only kern documents are checked.

### 3.4 Requests answered from the overlay

- `textDocument/formatting` on a document: the result is `null` when
  the document's text does not parse; otherwise an array that is empty
  when the text is already in canonical form and else holds one
  `TextEdit` turning the text into `format`'s output. The request's
  formatting options are ignored. A URI that is not a document gets
  error `-32602` (invalid params).
- `textDocument/hover` on a kern document: §4 R6. Otherwise `null`.
  Hover and formatting read the overlay as it is when the request is
  handled, whether or not a check is pending.
- `textDocument/codeAction` on a kern document: §4 R7. Otherwise `[]`.

### 3.5 Exit

The server exits on the `exit` notification, with status 0 when
`shutdown` was received and 1 otherwise, and on end of input, with the
same rule. It exits without writing a partial message.

## 4. Requirements

**R1 — Check per document.** Every `didOpen`, `didChange` and
`didClose` marks every kern document as stale. When the server
finishes handling the last message of a batch, it checks the program
(§2) of every stale kern document, in byte order of the document's
URI, once each, and the documents are no longer stale. Messages that
arrive while a check runs belong to the next batch. A batch of any
number of document messages therefore causes at most one check per
kern document. The project manifest (`kern.mod`) and every directory
listing are read from disk at each check, never from the overlay, and
a change on disk alone does not mark a document stale.

**R2 — Diagnostics published.** For each check of *D*'s program, the
server sends `textDocument/publishDiagnostics` for every file of the
program and every file a diagnostic names, in path order, each URI
once: the URI the client used when the file is a document, else the
`file` URI of the absolute path; the `version` of the document when the
file is one. A URI published for *D* earlier but absent this time is
published with an empty array when no other document's last check
published it, and left as it is otherwise (as R4 does on close). A URI's publication replaces its
previous one whichever document caused it. A diagnostic whose file is
empty or is not a file of the program and not the project manifest is
published on *D* at line 0, character 0, both ends, its message
prefixed by its file name and `": "` when the name is not empty. The
moves happen first: the files published are the program's files and
the files the diagnostics name after the moves, so a moved diagnostic
causes no publication on its original file.

**R3 — Diagnostic shape.** Each kern diagnostic becomes one LSP
diagnostic with `range` from its span (R8), `severity` 1 for `error`
and 2 for `warning`, `code` its code, `source` `"kern"`, `message` its
message (its code when the message is empty) followed by
`" (rule <rule>)"` when its rule is not empty, and
`relatedInformation` with one entry per related location: §16 places a
related location in the diagnostic's own file, so its URI is the URI
the diagnostic is published on and its range is converted against that
file's text; the label is the entry's message. A diagnostic R2 moves to
*D* carries no related information. The array keeps the compiler's
order.

**R4 — Close.** On `didClose` of *D*, every URI published for *D* that
no remaining document's last check published is published with an
empty array; the other documents R1 names are re-checked.

**R5 — Errors are contained.** A panic or internal error of the
compiler during a check is one diagnostic of code `INTERNAL` on *D* at
line 0, character 0 with the error's text as its message, and a
`window/logMessage` of type 1 with the same text; during a request it
is error `-32603` (internal error) with the error's text. The server
keeps running in both cases.

**R6 — Hover.** For a hover position in a kern document *D*, the server
resolves *D*'s program and takes the innermost name of *D* whose span
contains the position's byte offset, an offset equal to the span's end
included; a name is an identifier the resolver resolved to a symbol,
or the declaration of a local, parameter or binding of *D* as the
resolver records it (the span `explain-ownership` reports). The result
is `null` when there is none; of two names with the same span, the
resolved identifier wins. Otherwise the result's `range` is the
identifier's span and its `contents` is Markdown: a `kern` code block
holding one line,
- for a local, parameter or access binding: the kind (`local`,
  `param` or `binding`), the convention when it has one (`view`,
  `edit`, `own`, `out`), the name, `: ` and its type;
- for a function: `func`, its module and name joined by `.`, `: ` and
  its function type;
- for any other symbol: its kind (`type`, `const`, `global`, `module`
  or `type parameter`) and its name;

then, for a local, parameter or access binding when the program has no
diagnostics, the explain-ownership place of its declaration (§16): a
line `state: <state>`, one line per access `- <kind> at line <n>`, a
line `dropped at line <n>` when it has a drop point, and the contract's
clauses when it has one, each as `requires`, `ensures` or `invariant`
and its source text. Line numbers are 1-based lines of the file the
place names. When the program has diagnostics, only the code block is
shown.

**R7 — Quick fixes.** For `textDocument/codeAction` on a kern document
*D*, when *D* is stale the server first checks *D*'s program as R1
does (publishing as R2 does) and *D* is no longer stale; other stale
documents wait for the end of the batch. The result
holds one action per diagnostic last published on *D*'s URI that has a
fix and whose range intersects the request's range (ranges intersect
when neither ends before the other starts; touching ends count), in
published order, unless the request's `context.only` is present and
names neither `quickfix` nor a prefix of it (`""` or a kind that
`quickfix` starts with followed by `.`), when the result is `[]`:
`title` `Apply the compiler's fix for <code>`, `kind`
`quickfix`, `diagnostics` the LSP diagnostic, and an `edit` whose
`changes` map the URI of each file the fix changes, chosen as R2
chooses a published file's URI, to one `TextEdit` that turns
the file's current text into the result of applying the fix
(`apply-structured-edit`'s applier, `compiler/edit`) to the program's
files. A file whose text the fix leaves unchanged is not in
`changes`, and a fix the applier rejects or that changes no file gives
no action. A client that did
not declare `codeActionLiteralSupport` gets `[]`.

**R8 — Positions.** A byte offset becomes a position of line (0-based;
`\n`, `\r\n` and a lone `\r` each end a line) and character (the count
of the negotiated unit from the line's start: UTF-8 bytes, or UTF-16
code units, where a character outside the Basic Multilingual Plane
counts 2). A position becomes an offset by the inverse; a line past the
last is the end of the text, a character past its line's end is the
line's end, and a character inside a multi-unit character is that
character's start. An offset between the `\r` and the `\n` of a
`\r\n` is the end of its line, the position of the `\r`. An offset inside a multi-byte character or past the
end of the text is first moved to the start of that character or to
the end of the text. A text that is not valid UTF-8 counts each invalid
byte as one unit in both encodings.

**R9 — Minimal edits.** Every `TextEdit` the server sends (R7, §3.4)
is computed in this order: first the longest common prefix of the old
and new text, shortened to a UTF-8 character boundary of both; then
the longest common suffix of the remainders after that prefix, so the
two never overlap in either text, also shortened to a character
boundary of both. Neither end of the replaced range falls between the
`\r` and the `\n` of a `\r\n` in the old text: a prefix that would end
there or a suffix that would start there is shortened by one byte. The edit replaces the old text between prefix and
suffix with the new text between them. When the texts are equal there
is no edit.

**R10 — No other machine-API change.** The responses of every §16
command are byte-identical to those before this change for the
repository's tests, except `explain-ownership` where R13 changes a
place's accesses, drop point and contract.

**R13 — Explain-ownership reads the declaring function.** Found while
building R6: `explain-ownership` matched a place's symbol to IR locals
by name in every function of the module, library functions included,
so a local `v` of `main` reported the accesses of a parameter `v` of
another function and the contract of a library method. A place's
accesses, drop point and contract now come only from the declaring
function: of the IR functions of the place's file that are not library
code and whose span holds the declaration, those with the narrowest
span (the instantiations of one function; a closure is narrower than
the function it is written in). The set of places and their `loc`,
`owner`, `state` and `label` are unchanged. A test pins the two-function
case.

**R11 — Editor documentation.** `docs/editors.md` shows how to start
the server from Neovim (0.11 or later, built-in client), Helix and
Emacs (eglot), including the `.kern` file type, and what the server
does and does not provide. `README.md` names the command and links the
page.

**R12 — Cost.** A check of a program costs the server the compiler work
of `kern check` on the same files, plus reading the files and converting
positions. No timing bound is set; the report measures the
time from a `didChange` to its diagnostics on the repository's largest
multi-module example.

## 5. Acceptance criteria

1. Unit tests cover R8 in both encodings: ASCII, two-, three- and
   four-byte characters, `\r\n` and lone `\r`, positions past a line's
   end and past the last line, offsets inside a character, and invalid
   UTF-8. R9 is tested on equal texts, a change at the start, the end
   and the middle, and a change next to a multi-byte character.
2. Protocol tests run the server in-process over pipes and cover the
   lifecycle and error codes of §3.1, framing errors of §3.2, R1's
   coalescing (three changes placed in the server's input queue
   before its loop starts are one batch by the glossary's definition
   and give one check, counted through a test hook), R2's empty republication, R4, R5 (a panic
   injected through a test hook), R6 on a local, a parameter, a
   function and a use in a program with and without diagnostics, R7
   on a fix in the checker and one in the resolver, and formatting.
3. One test builds the `kern` binary, starts `kern lsp` on a temporary
   two-module project with a `kern.mod`, and sees an imported module's
   diagnostic published on that module's file URI, an unsaved buffer's
   text checked instead of the file on disk, and exit status 0 after
   `shutdown` and `exit`.
4. The existing explain-ownership tests pass unchanged (R10), and a
   new one shows a local and a same-named parameter of another
   function each reporting only their own function's accesses (R13).
5. A headless Neovim session starts the server on a repository
   example, receives its diagnostics and formats a file; the report
   records the session's commands and outcome. This step is manual and
   not part of `go test`.
