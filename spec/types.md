# kern — Types, modules and generic constraints (Phase 1 subset)

## Purpose
This document is the normative language reference for name resolution, the type system and generic constraints of the Phase 1 subset, companion to `grammar.ebnf`. The specification (`specs/initial/AI_Native_Systems_Language_SPEC.md`) §4.1 and §5 take precedence where they overlap.

## Non-goals
Ownership and access rules (spec §5, restated in `docs/ownership.md`), execution semantics (spec §4.1), the machine API (spec §7, §16), dynamic dispatch, escaping closures, and any form of implicit conversion are out of scope here.

## Acceptance criteria and testing
Every rule below that names a diagnostic code has a rejecting fixture under `tests/negative/` that the `kern check` command rejects with exactly that code, and an accepting twin; the Go test suite under `tests/` enforces this through the registry coverage test (PLAN.md §0). A rule without a code is tested by the accepting fixtures and the example programs that depend on it.

## Requirements (normative rules)

### 1. Modules and resolution
- One directory is one module: its source files are exactly the files in that directory whose name ends in `.kern`, in lexicographic order, subdirectories excluded. Its name is declared by `module <name>` in every file, and all files must agree (`MODULE_MISMATCH`).
- The **project root** is the nearest ancestor directory of the first file named in the request that contains a file named `kern.mod`; when none exists, it is the current working directory of the `kern` process. Request file paths are resolved relative to that process's working directory.
- `import "path"` makes the module at `path` (relative to the project root) available under the alias that is its last path component: `import "std/thread"` then `thread.spawn(...)`. Imports are not transitive. An import whose alias equals another import's alias or a module-level declaration of the importing module is `NAME_DUPLICATE`.
- Every declaration in a module is visible to every file of that module. For a user module, a type, interface, constant or module-level function whose name starts with a capital letter is exported and all others are module-private (`NAME_PRIVATE` on use from another module); methods are visible wherever their type is visible; a struct field is exported iff its name starts with a capital letter; a union's variants are exported together with the union (a variant has no separate visibility). The implicit `std/core` module and every `std/*` module export every declaration regardless of capitalization, which is why `Box.new`, `thread.scope` and `len` are usable from every module.
- Name lookup is lexical: block scope, then enclosing blocks, then function parameters, then module declarations, then imported module aliases. Shadowing a name in an inner block is permitted; declaring a name twice in one scope is `NAME_DUPLICATE`; shadowing a parameter is `NAME_SHADOWS_PARAM`. An unresolved name is `NAME_UNRESOLVED`.
- Built-in names (`Result`, `Option`, `Box`, `Vec`, `Arena`, `Pool`, `Slab`, `Shared`, `Mutex`, `Condvar`, `Atomic`, `RawPtr`, `thread`, `trap`, `Allocator`, `SystemAllocator`, `len`, `zero`) live in the implicit module `std/core`, imported by every module. `len(x)` yields the `usize` length of an array, slice or sequence access; `zero[T]()` yields the all-zero value of a **zeroable** type `T` (inferred from the expected type when omitted), and is the only way to initialize an array without naming every element. A type is zeroable iff it is an integer, float or `bool` type, `RawPtr[U]`, a container handle, or a struct, union or array whose every field or element type is zeroable; function types are not zeroable (an all-zero function value would not be callable), so `zero` on a type containing one is `CONSTRAINT_UNSATISFIED`. The all-zero value of a union is variant 0 with zeroed payload.

### 2. Types
- Primitive: `i8 i16 i32 i64 isize u8 u16 u32 u64 usize f32 f64 bool`; the unit type `()`.
- Nominal: `struct`, `union` (tagged), declared per `grammar.ebnf`. Types are nominal; two declarations are never the same type.
- Arrays `[N]T` (N a non-negative integer literal), slices `[]T` (unsized; only reachable through `view[]T`/`edit[]T` or container APIs).
- Access types: `view[T]`, `edit[T]`, `view[]T`, `edit[]T` are second-class (spec §5.2) and must appear only as parameter types, as return types under the elision rule, in `with`/`match`/`for` bindings, and inside `view struct` (reserved extension; rejected in Phase 0 with `ACC_ESCAPE_STORE` unless the extension is enabled).
- Function types `func(P...) -> R` are `Copy` and capture nothing. A function name used as a value has its declared function type. A generic function must be instantiated (`f[T]`) before use as a value.
- `RawPtr[T]` is an unsafe raw pointer; `Copy`; dereference, arithmetic and casts are unsafe operations.
- The conventions `own T` and `out T` are not types: they are parameter modifiers (spec §5.2). A parameter without a modifier and without an access type is passed by value: `Copy` values are copied, move-only values are moved (spec §5.1), which is the same as `own`. The explicit `own` keyword is required on parameters of move-only type so that transfer is visible (`PARAM_OWN_REQUIRED`); it is optional on `Copy` types.

### 3. Copy, Send, Share, Interior
Derived structurally per spec §5.1, §5.4, §5.5:
- `Copy`: primitives, `RawPtr`, function types, container handles; aggregates iff no destructor, no opt-out (`struct Name nocopy { }`), and every field `Copy`.
- `Send`: primitives, function types, and aggregates iff every field is `Send`; `RawPtr` is not `Send`; `unsafe assert Send for T` opts in.
- `Share`: primitives, function types, and aggregates iff every field is `Share`; `RawPtr` is not `Share`; `Interior` types are `Share` only by `unsafe assert Share`.
- `Interior`: declared by `unsafe assert Interior for T`; propagates to any aggregate containing `T`.
Assertions are top-level declarations: `unsafe assert Send for TypeName` (parsed as a decl; permitted only in a module that declares `TypeName`).

### 4. Generics
- Type parameters are declared on structs, unions, interfaces, impls and functions: `[T: Copy + Ord, U]`.
- Constraints are the built-in markers `Copy`, `Send`, `Share` and nominal interfaces. An unconstrained parameter admits every type but allows only moves, `drop`, and passing by convention; calling a method requires the interface constraint (`CONSTRAINT_REQUIRED`).
- A method may add constraints to its impl's type parameters with a `where` clause after its return type (`func extend(edit self, src: view[]T) where T: Copy`): inside the body the parameter has the added constraints, and a call whose receiver's type argument does not satisfy them is `CONSTRAINT_UNSATISFIED`, so the method exists for every instantiation of the type and is callable for those that satisfy the bound (a conditional implementation of one method, where `impl` itself is unconditional). A `where` bound on one of the function's own type parameters is the same as writing the constraint in its parameter list; a name that is neither is `NAME_UNRESOLVED`.
- Instantiation is explicit (`f[u64](x)`) or inferred from argument types by first-order unification over the parameter types; failure is `TYPE_INFER_FAILED`. A type argument that does not satisfy a constraint is `CONSTRAINT_UNSATISFIED`.
- Phase 0 checks generic bodies once, against their constraints, and does not instantiate them. From Phase 1 every instantiation is specialized to a monomorphic intermediate representation before validation.
- Interfaces: `interface Name { func m(view self, ...) -> R }`; `impl Name for T { ... }` must provide every method with an identical signature (`IMPL_INCOMPLETE`, `IMPL_SIGNATURE_MISMATCH`). There is no dynamic dispatch; an interface name is not a type.
- At most one `impl Name for T` exists per interface and type declaration, and a type's inherent methods and interface methods share one namespace: a second implementation of the same interface for the same type, or two methods of the same name on one type, is `NAME_DUPLICATE`. Method lookup on a value therefore never has more than one candidate.

### 5. Type checking of expressions (summary)
- Integer literals without suffix take the type required by context, else `i64`; float literals default to `f64`. No implicit conversions anywhere (`TYPE_MISMATCH`).
- Arithmetic operators require both operands of the same integer or float type. `== !=` require two operands of the same type and are defined for primitives, `RawPtr`, handles and `Copy` aggregates (field-wise); `< <= > >=` require two operands of the same integer or float type; every comparison yields `bool`.
- `&&` and `||` require `bool` operands and **short-circuit**: the right operand is evaluated only when the left operand does not determine the result. For ownership and initialization the right operand is therefore a conditional path, joined with the path that skipped it exactly as the two branches of an `if` are (`OWN_CONDITIONAL_MOVE`, `INIT_CONDITIONAL`).
- `expr?` requires `Result[T, E]` and a function return type `Result[_, E]` with the same `E` (`TYPE_QUESTION_MISMATCH`), yielding `T`.
- A call's argument conventions are determined by the callee's parameter list: a `view[T]`/`edit[T]` parameter takes a place (or an existing access); an `own T` parameter takes a value; an `out T` parameter takes `out <ident>` naming an uninitialized local or a container-provided slot.
- `match` must be exhaustive over the union's variants or end with `_` (`MATCH_NOT_EXHAUSTIVE`).
