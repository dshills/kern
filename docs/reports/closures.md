# Non-escaping closures

**Date:** 2026-10-28. **Decides:** the option ADR-010 deferred ("non-escaping
closures as second-class values under the rules of §5.3"), which ADR-013
lists among the prerequisites of a self-hosted compiler.

## What was added

- **Syntax.** A parameter type `closure(P...) -> R` and closure expressions
  `|a, b| expr`, `|a: T| { ... }` and `|| expr` (`spec/grammar.ebnf`:
  `closure_expr`, and the `closure` alternative of `type`; `closure` is a
  contextual keyword before `(`).
- **Typing** (`compiler/resolve`). A closure expression is typed by the
  closure parameter it is passed to: parameter types and conventions come
  from the closure type, declared types must be identical, the body is
  resolved in a scope nested in the enclosing function's with returns typed
  by the closure's result, and the enclosing symbols the body uses are
  recorded as captures (`ClosureInfo`), by edit where the body assigns to,
  takes from, replaces, moves or passes as an `edit` or `out` argument a
  place rooted at them, by view otherwise. Closure types are second-class:
  not `Copy`, `Send` or `Share`; allowed only as a parameter's own type; a
  field (`ACC_ESCAPE_STORE`), a local (`ACC_UNBOUND_PROJECTION`), a return
  type (`ACC_ESCAPE_RETURN`), a type argument and a thread spawn
  (`SEND_NOT_SEND`) all reject them. A closure cannot return an access.
- **Ownership** (`compiler/check`). At the call, each capture is an access
  (view or edit) live until the call returns, so it conflicts with the
  call's other arguments under §5.4 like any argument would
  (`tests/negative/closure_conflict`). The body is checked as a function of
  its own whose captured symbols are initialized access-like roots: a view
  capture is read-only and a captured variable cannot be moved out
  (`OWN_PARTIAL_MOVE`). A closure parameter is used, never consumed, by a
  call or by passing it on. Unsafe inside the body counts for the enclosing
  function's contract.
- **Lowering** (`compiler/lower`, `compiler/ir`). A closure body becomes a
  function of its own (`Func.Closure`, named after its enclosing function)
  whose first `Captures` parameters are the captured accesses and whose
  remaining parameters are the closure's; the value is a `MakeClosure`
  over accesses opened on the captured places, ended with the call's other
  argument accesses. A call through a closure parameter is the existing
  call through a function value. The validator requires the captures to be
  open accesses and rejects a stored closure; closure-typed locals need no
  destruction. The inliner leaves a callee with a closure parameter, and a
  closure function, as calls (the copy would store the closure in a slot);
  the proof pass forgets what a closure may write through its edit captures.
- **Engines.** The interpreter's closure value is the function name and
  the captured accesses, prepended to the arguments at every call. Natively
  a closure value is `{ ptr, ptr }`: the function and an environment, a
  frame slot of the capture pointers; the closure function takes the
  environment first and loads its captures from it in its prologue.
- **Tooling.** The formatter prints both closure forms; `show-effects`
  labels a call through a closure `unknown`, as through a function
  pointer; the DWARF line table covers closure functions.

Fixtures: `tests/run/closure_basic` (view, edit, binding, parameter and
closure-parameter captures, expression and block bodies, a closure passed
on, a nested closure, in both engines and both configurations), and the
pairs `closure_store`, `closure_bind`, `closure_return`, `closure_conflict`,
`closure_move`, `closure_spawn`, `closure_context` under `tests/negative`.
The inliner's eligibility table gains the closure-parameter case.

## Not done

- A function name is not a closure: passing `f` where a closure parameter
  is expected is a type error. Coercing it would need a thunk per function,
  and nothing asked for it.
- Capture is by access only: a closure cannot take ownership of a captured
  variable (`move` and `drop` are rejected). A closure parameter cannot be
  `out` (`TYPE_MISMATCH`): out storage is initialized by the function that
  declares it, and a closure has no initialization obligation of its own. The `(func, own context)` pattern of
  ADR-010 remains the way to hand a value to a callback.
- No workload was rewritten with closures; the feature's measurement is
  the fixture harness, not the benchmark report.
