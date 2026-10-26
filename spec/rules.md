# kern diagnostic codes

Generated from `compiler/diag/codes.go` by `go generate ./compiler/diag`; do not edit.
Rule identifiers are `section.bullet` into the specification (`T.n` = spec/types.md §n, `P.0` = PLAN.md §0).

| Code | Rule | Category | Phase | Summary |
|---|---|---|---|---|
| `ACC_AMBIGUOUS_SOURCE` | 5.3.4 | lang | 0 | returned access with more than one candidate source parameter |
| `ACC_CONFLICT` | 5.4.4 | lang | 0 | conflicting simultaneously live accesses |
| `ACC_ESCAPE_RETURN` | 5.2.2 | lang | 0 | second-class access returned outside the elision rule |
| `ACC_ESCAPE_STORE` | 5.2.2 | lang | 0 | second-class access stored in a struct, union, array or container |
| `ACC_LIVE_ACROSS_SPAWN` | 5.5.3 | lang | 0 | access live across a thread spawn |
| `ACC_MULTI_ACCESS_SAFE` | 5.3.4 | lang | 0 | tuple of accesses returned from a function without unsafe |
| `ACC_PERMISSION_ESCALATION` | 5.3.4 | lang | 0 | access stronger than its source |
| `ACC_RELEASE_ESCAPE` | 5.3.4 | lang | 0 | access carrying a pending release function returned |
| `ACC_UNBOUND_PROJECTION` | 5.3.2 | lang | 0 | projection bound with := instead of with/match/for |
| `API_FILE_NOT_FOUND` | 7.4 | api | 0 | a request file cannot be read |
| `API_INVALID_SPAN` | 7.4 | api | 1 | span does not name a request file or violates the span rules |
| `API_MALFORMED_REQUEST` | 7.4 | api | 0 | request is not valid JSON |
| `API_MISSING_SPAN` | 7.4 | api | 1 | explain-ownership requires a span |
| `API_NO_FILES` | 7.4 | api | 0 | request names no files |
| `API_TARGET_CONFLICT` | 7.4 | api | 2 | CLI --target differs from the request target |
| `API_UNKNOWN_COMMAND` | 7.4 | api | 0 | command is not one of the §7 commands |
| `API_UNSUPPORTED_SCHEMA` | 7.4 | api | 0 | schema_version is not supported |
| `ASSIGN_IMMUTABLE` | T.5 | lang | 0 | assignment through a view or to a non-place |
| `ATOMIC_INVALID_ORDERING` | 5.5.5 | lang | 0 | memory ordering not permitted for this atomic operation |
| `CALL_ARITY` | T.5 | lang | 0 | wrong number of arguments |
| `CLI_INVALID_FLAGS` | P.0 | cli | 0 | invalid or incompatible command-line flags |
| `CONSTRAINT_REQUIRED` | T.4 | lang | 0 | method call on a type parameter without the interface constraint |
| `CONSTRAINT_UNSATISFIED` | T.4 | lang | 0 | type argument does not satisfy a constraint |
| `EDIT_INVALID_SPAN` | 16.2 | edit | 4 | structured-edit span or offset is invalid |
| `EDIT_OVERLAP` | 16.2 | edit | 4 | structured-edit ops overlap |
| `EDIT_UNRESOLVED_SYMBOL` | 16.2 | edit | 4 | rename_symbol span is not a resolvable declaration |
| `FFI_UNSUPPORTED_TYPE` | 7.2 | lang | 0 | type not permitted in an extern "C" signature |
| `IMPL_INCOMPLETE` | T.4 | lang | 0 | impl lacks an interface method |
| `IMPL_SIGNATURE_MISMATCH` | T.4 | lang | 0 | impl method signature differs from the interface |
| `INIT_CONDITIONAL` | 5.2.3 | lang | 0 | variable initialized on one path but not another |
| `INIT_FIELD_OF_UNINITIALIZED` | 5.1.8 | lang | 0 | field assigned on an uninitialized local |
| `INIT_OUT_NOT_SET` | 5.2.1 | lang | 0 | out parameter not initialized on a normal-return path |
| `INIT_OUT_RESULT_UNHANDLED` | 5.2.3 | lang | 0 | result of a fallible out call not consumed by ? or match |
| `INIT_UNINITIALIZED_READ` | 5.1.7 | lang | 0 | read before initialization |
| `INTERNAL` | 7.failure | internal | 0 | internal compiler error |
| `MATCH_NOT_EXHAUSTIVE` | T.5 | lang | 0 | match does not cover every variant |
| `MODULE_MISMATCH` | T.1 | lang | 0 | files of one directory declare different module names |
| `NAME_DUPLICATE` | T.1 | lang | 0 | name declared twice in one scope |
| `NAME_PRIVATE` | T.1 | lang | 1 | module-private name used from another module |
| `NAME_SHADOWS_PARAM` | T.1 | lang | 0 | local shadows a parameter |
| `NAME_UNRESOLVED` | T.1 | lang | 0 | name not found |
| `OWN_CONDITIONAL_MOVE` | 5.1.5 | lang | 0 | variable moved on one path but not another |
| `OWN_DESTRUCTURE_DESTRUCTOR` | 5.3.1 | lang | 0 | owned match subject has a destructor |
| `OWN_PARTIAL_MOVE` | 5.1.6 | lang | 0 | move out of a field, element or access binding |
| `OWN_USE_AFTER_MOVE` | 5.1.4 | lang | 0 | value used after it was moved |
| `PARAM_OWN_REQUIRED` | 5.2.1 | lang | 0 | move-only parameter must be declared own |
| `PLACE_REQUIRED` | T.5 | lang | 0 | a place expression is required here |
| `SEND_NOT_SEND` | 5.5.2 | lang | 0 | value moved to another thread is not Send |
| `SHARE_NOT_SHARE` | 5.5.3 | lang | 0 | value viewed from another thread is not Share |
| `SYNTAX` | 4.0 | lang | 0 | syntax error |
| `TYPE_ARITY` | T.4 | lang | 0 | wrong number of type arguments |
| `TYPE_INFER_FAILED` | T.4 | lang | 0 | type arguments could not be inferred |
| `TYPE_MISMATCH` | T.5 | lang | 0 | expression type does not match the required type |
| `TYPE_QUESTION_MISMATCH` | T.5 | lang | 0 | ? used with a mismatched Result error type |
| `TYPE_TOO_MANY_VARIANTS` | 4.1.5 | lang | 0 | union has more than 65,536 variants |
| `UNSAFE_MISSING_CONTRACT` | 6.4 | lang | 0 | function containing unsafe has no contract block |
| `UNSAFE_OP_OUTSIDE_UNSAFE` | 6.4 | lang | 0 | unsafe operation outside an unsafe block |
