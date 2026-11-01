# Completion through constraints and loop scopes

**Date:** 2026-11-01. **Spec:** `specs/editor-complete-gaps/SPEC.md`
v0.1; **plan:** `specs/editor-complete-gaps/PLAN.md`. Closes two of the
gaps `docs/reports/editor-complete.md` listed.

## What changed

All in `compiler/lsp/complete.go`.

- **Type-parameter values (R1).** After `x.` where `x`'s type, through
  any access, is a type parameter, the server offers the `self` methods
  of each interface among its constraints, in declaration order; a name
  two constraints share is offered once, with the first constraint's
  signature, which is the method the resolver's call resolution picks
  (`compiler/resolve/calls.go`). Marker constraints (`Copy`, `Send`,
  `Share`) add nothing.
- **Where-clause bounds.** A method's `where T: map.Hashable` reaches
  completion with no extra work: the resolver records `self.a`'s type
  as the parameter as the body sees it, bounds included, so the
  fallback the spec's §5 allowed for (reading the enclosing function's
  `Where`) was not needed.
- **Primitive values (R2).** A `u64` (or any primitive) offers the
  methods impls on it declare, from the core module's `prim_<name>`
  declaration (for `u64`, `Hashable`'s `hash` and `eq`).
- **Header bindings (R3).** A `for` loop's variables, a `with`
  statement's names and a `match` arm's bindings are offered only with
  the cursor inside that statement's body, braces included. Their
  declarations sit in the block holding the statement, which is why the
  innermost-block rule offered them after the body. The server collects
  the binding positions of the function's statements and checks the
  cursor against the body each belongs to; every other local keeps the
  old rule.

## Tests

- `TestCompletionConstraints`: `T: map.Hashable` offers `hash` and `eq`;
  `T: map.Hashable + Named`, where the test interface `Named` declares
  its own `hash(view self, seed: u64)`, offers all three names with
  `Hashable`'s `hash` signature; `T: Copy` offers nothing; the
  where-clause method offers `hash` and `eq` on `self.a`; a `u64` local
  offers `hash` and `eq`; an undeclared receiver offers nothing.
- `TestCompletionHeaderScope`: the loop variable is offered just after
  the body's `{`, in the body and just before its `}`, and not after the
  loop; the `with` name and the arm binding inside their bodies and not
  after; a local declared before the loop in both places.
- Each test checks first that its source resolves with no diagnostics,
  so a mistyped probe source cannot pass by offering nothing.
- Run against the previous `complete.go`, the two tests fail with 15
  errors (every R1/R2 case and the three after-body probes);
  `TestCompletion` and the rest of `compiler/lsp` pass unchanged (R4).

## Not done

Several unfinished dots in one document (still listed in
`docs/reports/editor-complete.md`), and rename across programs
(`docs/reports/editor-names.md`), which needs the server to load every
program of a workspace rather than the one holding the document.

## Review

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...`
clean. speccritic: no critical finding after one revision (collision
order, the where-clause case, an out-of-scope section, unresolved
receivers). plancritic: EXECUTABLE_WITH_CLARIFICATIONS, 93; its warning
(collision detail, unresolved receiver, brace probes) folded into the
tests.

prism (`--reasoning-effort medium`), three low findings:
- *A doc comment detached from `stripAt`*: fixed.
- *`stripAt` slicing past an unterminated marker*: fixed; it panics with
  a message naming the cause.
- *The access-unwrapping loop has no cycle guard*: not adopted. A
  `resolve.Access` is built around an element type the resolver has
  already constructed, so a chain cannot point back to itself; `ownerOf`
  in `nav.go` relies on the same invariant with the same loop.

realitycheck (`compiler/lsp` as the code root; the whole repository's
index is truncated past its limit): PARTIALLY_ALIGNED, score 100, no
drift or violation; every item is UNCLEAR because it sees symbol names
without bodies, so it confirms only that the planned functions and tests
exist. verifier (offline, with this spec and plan): no critical or high
gap, and no finding in `compiler/lsp`.
