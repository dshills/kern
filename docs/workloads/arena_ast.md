# Workload: arena_ast

## Summary
An expression AST allocated in an `Arena[Expr]`, built by a parser, walked by
an evaluator and a constant folder, and freed as a whole. Parent pointers and
sharing of subtrees are plain handles. This is the §5.8 case "self-reference,
parent pointers, cycles" in its most common form.

## Classification
expressible in the core

## Data structures and ownership graph
```text
union Expr { Num(i64); Var(u32); Add(lhs: ExprId, rhs: ExprId); Mul(lhs: ExprId, rhs: ExprId); Let(name: u32, val: ExprId, body: ExprId) }
struct Ast { nodes: Arena[Expr]; root: ExprId; parent: Vec[ExprId] }
```
- `Ast` owns the arena; the arena owns every `Expr`; every edge (`lhs`, `rhs`,
  `val`, `body`, `root`, `parent[i]`) is a `Copy` `ArenaId[Expr]` (§5.6).
- Cycles are allowed by the type (a handle may name its own slot) and are
  harmless to memory safety; the walker guards against them with a visited
  bitmap, which is what a C AST would also need.
- Destruction is one call: dropping `Ast` drops the arena, which destroys
  every live `Expr` in slot order. No per-node free exists in `Arena`
  (that is `Pool`); the AST never needs it.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `a.nodes.alloc(edit self, own Expr) -> Result[ExprId, AllocErr]` | `edit` | the only write during parsing |
| `eval(a: view[Ast], id) -> i64` | `view` | `match a.nodes.get(id)` binds arm views; handles read out of arms are `Copy` |
| `fold(a: edit[Ast], id) -> ExprId` | `edit` | rewrites a node in place via `with e := a.nodes.at(id) { replace(e, Expr.Num(v)) }` |
| parent lookup | `a.parent.get(usize(id))` | handle, not a reference |
No `get2`: a rewrite never edits two nodes in one statement (the fold reads
children first, closes the access, then writes the parent). No release accessor.

## Why this classification
Handles are `Copy` data that may be stored anywhere, including inside other
arena objects (§5.6); that is exactly what a tree with parent pointers needs.
The `match` on a projection (§5.3 form 2) binds payload fields as views for the
arm, and recursion passes the same `view[Ast]` down, so the evaluator holds at
most one access per activation. A node that held `view[Expr]` fields would be
`ACC_ESCAPE_STORE`; the arena design never wants it. The §5.6 extension is not
required, and the workload contains no `unsafe`.

## Sketch
```text
module ast_bench

func eval(a: view[Ast], env: view[]i64, id: ExprId) -> i64 {
    match a.nodes.get(id) {                             // arm bindings are views for the arm
        Num(n)          => return n                     // i64 is Copy
        Var(i)          => return env[usize(i)]
        Add(l, r)       => return eval(a, env, l) + eval(a, env, r)   // l, r are Copy handles
        Mul(l, r)       => return eval(a, env, l) * eval(a, env, r)
        Let(n, v, b)    => return eval_let(a, env, n, v, b)
    }
}

func fold(a: edit[Ast], id: ExprId) -> Result[(), AllocErr] {
    var folded: Option[i64]
    match a.nodes.get(id) {                             // view access, ends at '}'
        Add(l, r) => folded = const_pair(a, l, r, 0u8)
        Mul(l, r) => folded = const_pair(a, l, r, 1u8)
        _         => folded = Option.None
    }
    match move folded {                                 // owned subject, Option has no destructor
        Some(v) => with e := a.nodes.at(id) { replace(e, Expr.Num(v)) }   // write after read ended
        None    => {}
    }
    return Result.Ok(())
}

func parse_add(p: edit[Parser], a: edit[Ast]) -> Result[ExprId, ParseErr] {
    lhs := parse_mul(p, a)?
    while p.peek() == 43u8 {                            // '+'
        p.advance()
        rhs := parse_mul(p, a)?
        lhs = a.nodes.alloc(Expr.Add(lhs, rhs))?        // handles stored inside the node
    }
    return Result.Ok(lhs)
}
```
`fold` reads the children under one `view` access, lets it end, and only then
opens the `edit`; writing inside the first `match` would be `ACC_CONFLICT`.

## Benchmark notes
- C: a bump arena of `struct Expr` with `uint32_t` child indices, and a
  pointer-based variant as the lower bound.
- Rust: `Vec<Expr>` with `u32` indices (the idiomatic arena); `typed-arena`
  with `&'a Expr` references as the reference-holding comparison.
- Measure: parse throughput (nodes/s) on a 1e7-node synthetic program, eval
  throughput, fold pass, peak RSS (one arena chunk list; `show-layout` of `Expr`
  must match the C union), allocation count (arena chunks only), copies (zero),
  unsafe lines (zero), text bytes, compile time, bounds and generation checks
  per node visit.

## Open risks
- `Arena.get` performs a bounds check and, in debug, an identity check per
  visit; the eval loop is check-bound unless `dominated_by_check` proofs apply
  to repeated `get(id)` in one function.
- Error paths through `?` in `parse_add` leave allocated nodes in the arena
  (the arena is freed as a whole, so this is a space cost, not a leak; the
  report should state it).
