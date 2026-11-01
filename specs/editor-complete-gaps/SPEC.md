# Editor: completion through constraints and loop scopes

**Version:** 0.1 (2026-11-01). **Scope:** the language server's
completion (`compiler/lsp/complete.go`), its tests, `docs/editors.md`
and the reports `docs/reports/editor-complete.md` and
`docs/reports/editor-complete-gaps.md`. **Language impact:** none.
**Related:** `specs/editor-complete/SPEC.md` (R1–R4 there stand; this
spec changes its R2 "any other expression" case and its R3 "Locals"
rule).

**Review status.** speccritic (default model): no critical finding;
its warnings (collision order, where-clause test, out-of-scope section,
unresolved receivers, the fallback marked non-normative) folded in; the
acronym warnings name R*n*, IR and LSP, which the glossary defines.

**Glossary.** R*n* (R1–R6): requirement *n* of §2; in the related
spec, its own requirement *n*. IR: the compiler's LLVM intermediate
representation. LSP: the Language Server Protocol. Header binding: a name a
statement declares for its body only: a `for` loop's one or two
variables, a `with` statement's names, a `match` arm's bindings.
Constraint: an interface named in a type parameter's bound (`T:
Hashable`) or its function's or impl's where-clause, as the resolver
records it on the parameter's type (`resolve.TypeParamType.Constraints`).

## 0. Goals

Completion after a dot offers what the resolver accepts there for a
type-parameter or primitive value, and completion elsewhere stops
offering a header binding outside the body it belongs to. Non-goals: the
other gaps in `docs/reports/editor-complete.md` (several unfinished dots
in one document) and rename across programs
(`docs/reports/editor-names.md`), which needs workspace-wide loading.

## 1. Background

`memberItems` finds the type declaration behind a value's type with
`ownerOf`, which follows accesses to a `Named` type and otherwise gives
nothing, so a value of type `T` or `u64` gets no members. The resolver
resolves `x.m()` on a type parameter by searching its constraints'
interfaces in order (`compiler/resolve/calls.go`), and on a primitive
through the core module's `prim_<name>` type declaration that impls on
primitives fill. `localItems` keeps a declaration when its innermost
enclosing block encloses the cursor; a header binding's innermost block
is the one holding the statement, so it stays offered after the body.

## 2. Requirements

**R1 — Type-parameter values.** After a dot following an expression
whose type, through any access, is a type parameter: the `self` methods
of every interface among its constraints, each with its signature as
detail; a name provided by two constraints is offered once, with the
signature of the first constraint in declaration order that provides it
(the one the resolver's call resolution picks). The constraints are the
ones the value's recorded type carries, which inside a method include its
where-clause bounds on the owner's parameters. A
marker-only constraint (`Copy`, `Send`, `Share`) contributes nothing.
No fields are offered.

**R2 — Primitive values.** After a dot following an expression whose
type, through any access, is a primitive with a `prim_<name>`
declaration in the core module: that declaration's `self` methods.
A receiver with no recorded type (the text around the dot does not
resolve), or with a type none of R1, R2 or the named-type case covers,
gets no members, as before.

**R3 — Header bindings.** A header binding is offered only when the
cursor lies inside the body of the statement that declares it (from the
body's opening brace to its closing brace, inclusive). Every other local
keeps the rule of `specs/editor-complete` R3.

**R4 — Unchanged otherwise.** Named-type members, module members, type
statics, declarations and keywords are offered exactly as before; the
existing completion tests pass unchanged.

**R5 — Tests.** In `compiler/lsp`:
- a generic function `func f[T: Hashable](x: view T)`: `x.` offers
  `hash` and `eq` (the methods of `Hashable`); a parameter with two
  interface constraints offers both interfaces' methods; a `T: Copy`
  parameter offers nothing; inside `func h(view self) -> u64 where T:
  Hashable` of `impl Pair[T]` (with `struct Pair[T] { a: T }`),
  `self.a.` offers `hash` and `eq`;
- a `u64` local: `n.` offers the methods the prelude implements on
  `u64`;
- a `for` variable, a `with` name and a `match` arm binding are offered
  inside their bodies and not after them; a local declared before the
  loop is offered in both places.

**R6 — Documents.** `docs/reports/editor-complete.md` "Not done" loses
the two entries this spec closes and points to the new report;
`docs/editors.md`'s completion paragraph mentions type parameters;
`docs/reports/editor-complete-gaps.md` records the design, tests and
review.

## 3. Out of scope

Several unfinished dots in one document; rename across programs;
completion of interface statics or of a constraint's type parameters;
any change outside `compiler/lsp` and the documents of R6.

## 4. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...`
clean. The compiler's output is untouched (only `compiler/lsp` changes),
so no IR comparison is needed.

## 5. Risks

- **Where-clause bounds.** A method's where-clause adds bounds to the
  owner's parameters as the body sees them (`FuncDecl.Where`).
  *Implementation guidance (non-normative):* if the value's recorded
  type does not carry them, completion adds the
  enclosing function's where-clause bounds for a parameter of the same
  name and owner, still inside `compiler/lsp`; the report says which
  held.
