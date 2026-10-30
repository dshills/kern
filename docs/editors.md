# Editor support

`kern lsp` is a language server: it speaks the Language Server Protocol
3.17 on standard input and output, so any editor with an LSP client can
use it. It takes no arguments and writes its log lines to standard
error. The contract is `specs/language-server/SPEC.md`.

## What it provides

- **Diagnostics as you type.** Each open `.kern` file is checked as
  `kern check` would check it, with your unsaved edits: the file, the
  other files of its module in its directory, and every module they
  import, found from the project's `kern.mod` as on the command line,
  with the module's test files (`*_test.kern`), whose errors appear
  even when only another file of the module is open. A
  diagnostic in an imported module is shown on that module's file. The
  message is the compiler's sentence followed by its rule number, and
  the diagnostic's code is its §16 code (`OWN_USE_AFTER_MOVE`, ...).
- **Formatting.** The canonical form of `kern format`; the editor's
  indentation settings are ignored, as the format has none.
- **Quick fixes.** Where the compiler attaches a machine-applicable fix
  to a diagnostic, the editor offers it as a code action titled "Apply
  the compiler's fix for CODE".
- **Hover.** On a local, parameter or binding: its type and convention,
  and, when the program checks cleanly, what `explain-ownership`
  reports: its state, the accesses opened on it by line, where it is
  dropped, and the function's contract. On a function: its type. On
  other names: what kind of name it is.
- **Go to definition and find references.** For locals, parameters,
  bindings, functions, methods, types, interfaces, constants, globals
  and struct fields, across every file of the program, imported modules
  included. Prelude and `std/` declarations have no location to go to.
  A struct literal's field names (`Point { X: 1u64 }`) and the names in
  type annotations are not counted as references.
- **Checks only what changed.** Each open file's program is checked
  through the same per-module cache as `kern check --cache`, so an edit
  re-checks only the programs that contain the edited file.

It does not provide completion, rename, semantic highlighting or a
syntax grammar. Edits to `kern.mod` and new
files that are not yet saved in an imported module are seen when saved.

## Neovim (0.11 or later)

```lua
vim.filetype.add({ extension = { kern = "kern" } })
vim.lsp.config("kern", {
  cmd = { "kern", "lsp" },
  filetypes = { "kern" },
  root_markers = { "kern.mod", ".git" },
})
vim.lsp.enable("kern")
```

Format with `:lua vim.lsp.buf.format()`, see quick fixes with
`:lua vim.lsp.buf.code_action()`, hover with `K`.

## Helix

In `~/.config/helix/languages.toml`:

```toml
[language-server.kern]
command = "kern"
args = ["lsp"]

[[language]]
name = "kern"
scope = "source.kern"
file-types = ["kern"]
roots = ["kern.mod"]
comment-token = "//"
indent = { tab-width = 4, unit = "    " }
language-servers = ["kern"]
```

## Emacs (eglot)

```elisp
(define-derived-mode kern-mode prog-mode "Kern"
  (setq-local comment-start "// "))
(add-to-list 'auto-mode-alist '("\\.kern\\'" . kern-mode))
(with-eval-after-load 'eglot
  (add-to-list 'eglot-server-programs '(kern-mode "kern" "lsp")))
```

Then `M-x eglot` in a `.kern` buffer.

## Notes

- Positions are exchanged in UTF-8 when the editor offers it and in
  UTF-16 otherwise.
- Every edit re-checks every open `.kern` file once the editor pauses
  between messages; with many large files open this repeats work.
- The server holds at most 64 unhandled messages; an editor sends a few
  kilobytes per message, so this is no practical limit.
