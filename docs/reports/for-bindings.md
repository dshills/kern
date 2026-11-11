# Loop variables of Copy elements

**Date:** 2026-11-11. **Spec:** `specs/for-bindings/SPEC.md` v0.5.
**Plan:** `specs/for-bindings/PLAN.md`, three phases. **Follows:**
`docs/reports/match-bindings.md`, whose rule this gives to the other
binding form. **Hosts:** macOS 27.2 on arm64 and Ubuntu 24.04 on arm64 in
the `kern-linux-check` container.

## The defect

A `for` loop's variable over elements of a `Copy` type was a copy of each
element, in a writable local. So

```kern
func translate(data: edit[]u8, table: view[[256]u8]) {
    for b in data { b = table[usize(b)] }
}
```

was accepted and changed nothing in `data`, in the interpreter and
natively. Rule 5.3.1 said the variable is a copy, so the compiler did
what the reference said; the write was lost all the same, and the same
text in a generic function wrote to the sequence. It was left out of the
`match` change as a separate decision.

## What was measured before deciding

On 2026-11-11, over the 2,127 kern files of `tests`, `examples`, `bench`
and `std`, a compiler changed to flag every write through such a variable
flags three files:

| File | What the loop does |
|---|---|
| `tests/run/release_accessor.kern` | `for x in a.all() { x = x * 2u64 }`, written without expecting anything of it |
| `bench/agent/runs/20261102-134710/t10_buffer_review-plain-3376311375/buf.kern` | the function above: an agent's answer to a task that asks for a buffer to be translated in place |
| `bench/agent/runs/20261101-165447/t04_image_ops-structured-808147416/image.kern` | `for p in dst { ... p = src[j] ... }`: an agent's copy of one image row into another |

Both agent programs mean to change the sequence, and neither did. No file
writes through a variable of a loop over a map.

A `for` keeps its sequence frozen for the whole loop whatever its variable
is, so the cost that ruled out "every binding an access" for `match` (88
files) does not arise here: no loop body could reach its sequence by name
to begin with.

## The decision

The owner chose, on 2026-11-11, "an edit when written", the rule chosen
for `match` the same day, over "copies that must not be written" and "no
change":

- A variable the body only reads is a copy, as before, and a function
  with no write through such a variable is compiled to the code it was
  compiled to.
- A variable the body uses as an edit is an edit of the element for each
  iteration, when the loop iterates an edit: an `edit` sequence, a mutable
  array, a mutable value of a type whose editing getter returns an edit
  for that name (a map's value, not its key). Otherwise that use is an
  error.
- A variable of a loop over a path of another Copy binding (a nested
  array, an arm's array payload) is derived from it, so the write reaches
  the place the outer binding came from.

## How it is built

- **Arrays and sequences.** The resolver marks the variable as it marks an
  arm's Copy binding; the machinery of the `match` change does the rest.
  The checker and the lowering already bound a loop variable as a local
  or as an access by what the resolver made it.
- **Cursor loops** (`for k, v in m`). These are desugared before their
  body is resolved, into a `with` over the type's getter and a generated
  statement that copies a `Copy` element out of its access. The copy is
  marked like any other Copy binding. When the body has been resolved and
  a marked copy turns out to have been written in a loop that is editable
  for it, the desugared loop is rebuilt in place as the editing loop it
  would have been for an element that is not `Copy`: the copy statement
  goes, the `with` binds the variable's own symbol, the call becomes the
  `_mut` getter and is resolved again. The body is resolved once.
- The fields that marked an arm's binding (`ArmCopy`, `ArmEdit`) are
  renamed `CopyBound` and `CopyEdit`, since they now serve both forms.
- No diagnostic code was added; `spec/rules.lock` is unchanged.

## Effect on existing programs

1. In a loop over an edit, the write now reaches the element.
2. In a loop over a view, and for a map's key, the write no longer
   compiles.
3. A map loop that writes its value and has a loop over the same map
   nested in it no longer compiles, as a loop that edits values that are
   not `Copy` never did.

The two recorded agent programs would behave differently if they were run
again with this compiler: their loops would now do what their authors
meant. Their recorded results stand as measured with the compiler of
their day; the runs are not re-scored.

## Verification

- **Fixtures.** `tests/run/for_binding_edit` (26 lines of output: the
  function above; arrays, slices, a `Vec` through `as_edit`; fields, `edit`
  parameters, `edit self` methods, `take`, `replace`, closures, `continue`
  and `break`; nested arrays; a `match` on the element; loops over an arm's
  binding and over a slice of it; a map's value; cursor types of the
  fixture's own with one name and with two, the two-name one with neither,
  the first, the second and both names written; generic functions beside
  their twin), `tests/run/for_binding_copy` (what does not change: a
  reading loop, a range loop that assigns to its variable, two reading
  loops nested over one map), nine `tests/negative/for_binding_*` (one
  diagnostic each), and `tests/run/release_accessor`, which prints the
  doubled items. The expected output of the two new run fixtures was
  written from the spec before they were run, and both matched on the
  first run, in the interpreter, a native debug build and a native release
  build.
- **Unit tests.** The resolver's decision for each loop variable and the
  derivation of a nested loop's variable; the rebuilt cursor loop, record
  by record, for a two-name type with each combination of written names
  and for `map.Map`; a cursor loop over an arm's binding
  (`compiler/resolve`); the hover text (`compiler/lsp`).
- **Corpus invariance** (spec A6). `kern check` on every `.kern` file of
  `tests`, `examples`, `bench` and `std`, 2,138 files, with the compiler of
  `1ac1914` and the changed one: the diagnostics differ for the nine
  negative fixtures this work adds, and for no other file. The two agent
  programs compile as before; what changes is what they do.
- **Generated code** (spec A7). `scripts/capture-ll.sh` with both
  compilers: 518 modules each, byte-identical except
  `tests_run_for_binding_edit` and `tests_run_release_accessor`, in both
  configurations, the two captured programs that write through a loop
  variable. Every benchmark workload is identical.
- **macOS.** `go test ./... -timeout 90m` passed, with the generated
  inventory regenerated first for the new fixtures. The sanitized corpus
  passed. `golangci-lint run ./...` reports nothing.
- **Linux.** `KERN_SANITIZE=1 scripts/linux-check.sh --run "./compiler/... -timeout 40m" --run "./tests -timeout 120m"`,
  with the new fixtures and unit tests named: 2,917 tests passed, none
  failed.
- **Review.** `prism review staged`: ten findings, all low. Taken: the
  `_mut` getter must return the view getter's element type for a name to
  count as editable; the permission update after a rebuild is skipped as a
  whole when the call did not resolve; the spec names F4's codes and lists
  every negative fixture. The rest were about test messages.
- `spec/rules.lock` is unchanged.

## Left as it is

- A range loop's variable is a local holding the counter's value;
  assigning to it changes neither the counter nor the number of
  iterations. It is not a binding to a place, so nothing is lost.
- `with x := p.x` is still refused although Rule 5.3.3 calls a field a
  projection (noted in `docs/reports/match-bindings.md`).
