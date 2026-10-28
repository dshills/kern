// Package diag defines kern's stable diagnostic codes, their rule numbers and
// categories, and the JSON encoding of the machine-API response (spec §7, §16).
//
//go:generate go run ./gen ../../spec/rules.md
package diag

// Category classifies a diagnostic code by the kind of test that must cover it
// (PLAN.md §0, "Diagnostic registry categories").
type Category string

const (
	CatLang     Category = "lang"     // source reject fixture + accepting twin
	CatAPI      Category = "api"      // JSON request fixture under tests/api
	CatEdit     Category = "edit"     // JSON request fixture under tests/api
	CatCLI      Category = "cli"      // contract test in cmd/kern
	CatInternal Category = "internal" // injected-failure test
)

// Code is a stable diagnostic code such as OWN_USE_AFTER_MOVE.
type Code string

// Info describes one registered code.
type Info struct {
	Rule     string   // rule identifier: spec section.bullet, e.g. "5.1.4"
	Category Category // test category
	Phase    int      // phase that introduces the code (0..5)
	Summary  string   // one-line human summary used in messages
}

// Registry is the single list of codes. A code absent from this map must not
// be emitted; tests enforce that every spec code is present and that every
// registered code has its required test from its introduction phase.
var Registry = map[Code]Info{
	// §5.1 unique ownership and moves
	"OWN_USE_AFTER_MOVE":          {"5.1.4", CatLang, 0, "value used after it was moved"},
	"OWN_CONDITIONAL_MOVE":        {"5.1.5", CatLang, 0, "variable moved on one path but not another"},
	"OWN_PARTIAL_MOVE":            {"5.1.6", CatLang, 0, "move out of a field, element or access binding"},
	"INIT_UNINITIALIZED_READ":     {"5.1.7", CatLang, 0, "read before initialization"},
	"INIT_FIELD_OF_UNINITIALIZED": {"5.1.8", CatLang, 0, "field assigned on an uninitialized local"},
	// §5.2 parameter conventions
	"INIT_OUT_NOT_SET":          {"5.2.1", CatLang, 0, "out parameter not initialized on a normal-return path"},
	"ACC_ESCAPE_STORE":          {"5.2.2", CatLang, 0, "second-class access stored in a struct, union, array or container"},
	"ACC_ESCAPE_RETURN":         {"5.2.2", CatLang, 0, "second-class access returned outside the elision rule"},
	"INIT_OUT_RESULT_UNHANDLED": {"5.2.3", CatLang, 0, "result of a fallible out call not consumed by ? or match"},
	"INIT_CONDITIONAL":          {"5.2.3", CatLang, 0, "variable initialized on one path but not another"},
	"PARAM_OWN_REQUIRED":        {"5.2.1", CatLang, 0, "move-only parameter must be declared own"},
	// §5.3 named access, projections, elision
	"OWN_DESTRUCTURE_DESTRUCTOR": {"5.3.1", CatLang, 0, "owned match subject has a destructor"},
	"ACC_UNBOUND_PROJECTION":     {"5.3.2", CatLang, 0, "projection bound with := instead of with/match/for"},
	"ACC_AMBIGUOUS_SOURCE":       {"5.3.4", CatLang, 0, "returned access with more than one candidate source parameter"},
	"ACC_RELEASE_ESCAPE":         {"5.3.4", CatLang, 0, "access carrying a pending release function returned"},
	"ACC_PERMISSION_ESCALATION":  {"5.3.4", CatLang, 0, "access stronger than its source"},
	"ACC_MULTI_ACCESS_SAFE":      {"5.3.4", CatLang, 0, "tuple of accesses returned from a function without unsafe"},
	// §5.4 aliasing
	"ACC_CONFLICT": {"5.4.4", CatLang, 0, "conflicting simultaneously live accesses"},
	// §5.5 concurrency
	"SEND_NOT_SEND":           {"5.5.2", CatLang, 0, "value moved to another thread is not Send"},
	"ACC_LIVE_ACROSS_SPAWN":   {"5.5.3", CatLang, 0, "access live across a thread spawn"},
	"SHARE_NOT_SHARE":         {"5.5.3", CatLang, 0, "value viewed from another thread is not Share"},
	"CONDVAR_NOT_GUARD":       {"5.5.4", CatLang, 0, "Condvar.wait argument is not the edit bound by a Mutex lock"},
	"ATOMIC_INVALID_ORDERING": {"5.5.5", CatLang, 0, "memory ordering not permitted for this atomic operation"},
	// §4.1 layout
	"TYPE_TOO_MANY_VARIANTS": {"4.1.5", CatLang, 0, "union has more than 65,536 variants"},
	"TYPE_PACKED_ATOMIC":     {"4.1.5", CatLang, 3, "packed struct holds an Atomic, whose cell must keep its natural alignment"},
	// §6 unsafe
	"UNSAFE_OP_OUTSIDE_UNSAFE": {"6.4", CatLang, 0, "unsafe operation outside an unsafe block"},
	"UNSAFE_MISSING_CONTRACT":  {"6.4", CatLang, 0, "function containing unsafe has no contract block"},
	// §7 FFI
	"FFI_UNSUPPORTED_TYPE": {"7.2", CatLang, 0, "type not permitted in an extern \"C\" signature"},
	// §6 thread-local storage
	"TLS_INVALID_DECL": {"6.5", CatLang, 2, "thread_local requires a Copy type and a literal or zero() initializer"},
	// general language errors (spec §4, types.md)
	"SYNTAX":                  {"4.0", CatLang, 0, "syntax error"},
	"MODULE_MISMATCH":         {"T.1", CatLang, 0, "files of one directory declare different module names"},
	"NAME_UNRESOLVED":         {"T.1", CatLang, 0, "name not found"},
	"NAME_PRIVATE":            {"T.1", CatLang, 1, "module-private name used from another module"},
	"NAME_SHADOWS_PARAM":      {"T.1", CatLang, 0, "local shadows a parameter"},
	"NAME_DUPLICATE":          {"T.1", CatLang, 0, "name declared twice in one scope"},
	"TYPE_MISMATCH":           {"T.5", CatLang, 0, "expression type does not match the required type"},
	"TYPE_ARITY":              {"T.4", CatLang, 0, "wrong number of type arguments"},
	"TYPE_INFER_FAILED":       {"T.4", CatLang, 0, "type arguments could not be inferred"},
	"TYPE_QUESTION_MISMATCH":  {"T.5", CatLang, 0, "? used with a mismatched Result error type"},
	"CONSTRAINT_UNSATISFIED":  {"T.4", CatLang, 0, "type argument does not satisfy a constraint"},
	"CONSTRAINT_REQUIRED":     {"T.4", CatLang, 0, "method call on a type parameter without the interface constraint"},
	"IMPL_INCOMPLETE":         {"T.4", CatLang, 0, "impl lacks an interface method"},
	"IMPL_SIGNATURE_MISMATCH": {"T.4", CatLang, 0, "impl method signature differs from the interface"},
	"CALL_ARITY":              {"T.5", CatLang, 0, "wrong number of arguments"},
	"MATCH_NOT_EXHAUSTIVE":    {"T.5", CatLang, 0, "match does not cover every variant"},
	"PLACE_REQUIRED":          {"T.5", CatLang, 0, "a place expression is required here"},
	"ASSIGN_IMMUTABLE":        {"T.5", CatLang, 0, "assignment through a view or to a non-place"},
	// machine API (§7, §16)
	"API_MALFORMED_REQUEST":  {"7.4", CatAPI, 0, "request is not valid JSON"},
	"API_UNSUPPORTED_SCHEMA": {"7.4", CatAPI, 0, "schema_version is not supported"},
	"API_NO_FILES":           {"7.4", CatAPI, 0, "request names no files"},
	"API_FILE_NOT_FOUND":     {"7.4", CatAPI, 0, "a request file cannot be read"},
	"API_MISSING_SPAN":       {"7.4", CatAPI, 1, "explain-ownership requires a span"},
	"API_INVALID_SPAN":       {"7.4", CatAPI, 1, "span does not name a request file or violates the span rules"},
	"API_TARGET_CONFLICT":    {"7.4", CatAPI, 2, "CLI --target differs from the request target"},
	"API_UNKNOWN_COMMAND":    {"7.4", CatAPI, 0, "command is not one of the §7 commands"},
	"EDIT_OVERLAP":           {"16.2", CatEdit, 4, "structured-edit ops overlap"},
	"EDIT_UNRESOLVED_SYMBOL": {"16.2", CatEdit, 4, "rename_symbol span is not a resolvable declaration"},
	"EDIT_INVALID_SPAN":      {"16.2", CatEdit, 4, "structured-edit span or offset is invalid"},
	// CLI (PLAN.md §0)
	"CLI_INVALID_FLAGS": {"P.0", CatCLI, 0, "invalid or incompatible command-line flags"},
	// internal
	"INTERNAL": {"7.failure", CatInternal, 0, "internal compiler error"},
}

// Valid reports whether c is registered.
func Valid(c Code) bool { _, ok := Registry[c]; return ok }
