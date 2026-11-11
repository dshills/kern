// kern freestanding runtime: the entries compiled kern code calls, with
// their C prototypes (specs/runtime/SPEC.md RA1). rt.c and f64text.c
// include this header; a function they define that is not declared here is
// static. The conventions are those of the hosted runtime's kern_rt.h: a
// (p, n) pair is a byte sequence, an `out`/`off` pair the address of a
// Result (out[0] the tag, the payload at out + off), and a trailing
// `const kern_loc *at` the location of the user-level call, where the entry
// raises its traps. Each prototype is preceded by its one-line contract.
#ifndef KERN_RT_FS_H
#define KERN_RT_FS_H

#include <stdint.h>
#include <stddef.h>

// KERN_RT_ABI is the version of this interface, the hosted runtime's
// (kern_rt.h); every image references the symbol kern_rt_abi_<KERN_RT_ABI>.
#define KERN_RT_ABI 3
extern const int32_t kern_rt_abi_3;

// kern_loc is the location of a user-level call: a byte span and the file
// (null: the program's file).
typedef struct { int64_t start, end; const char *file; } kern_loc;
typedef struct kern_sysalloc kern_sysalloc;

// The lowest address the stack may reach; set by the startup code and compared in every function prologue.
extern void *kern_stack_limit;

// ---- rt.c: startup, console, exit ----

// Initializes the runtime before the program runs: it sets the board up (kern_board_init); abi is ignored (it makes the image reference the ABI symbol).
void kern_start(const void *abi);
// Runs the program's main function and returns its result, the halt code.
int32_t kern_run_main(int32_t (*fn)(void));
// Writes v in decimal to the console.
void print_u64(uint64_t v);
// Writes v in decimal to the console.
void print_i64(int64_t v);
// Writes "true" or "false" to the console.
void print_bool(uint8_t v);
// Writes the n bytes at p to the console.
void print_str(const char *p, int64_t n);
// Writes the n bytes at p to the console (there is one device).
void eprint_str(const char *p, int64_t n);
// Writes a newline to the console.
void println(void);
// Writes the halt line and powers the machine off; never returns.
_Noreturn void kern_exit(int32_t code);
// Does nothing: there is one thread.
void kern_yield(void);

// ---- rt.c: traps ----

// Records the program's file name, reported by a trap that names none.
void kern_set_file(const char *name);
// Registers the program's line tables, from which a trap's line and column are computed.
void kern_set_lines(const void *t, int64_t n);
// Installs the trap handler and returns the previous one (null: the default).
void *kern_set_handler(void *fn);
// Raises a trap of the kind at the span in file (null: the program's): writes the record or runs the handler once, then halts with code 134; never returns.
_Noreturn void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file);

// ---- rt.c: the C library symbols LLVM may call ----

// Rounds toward zero.
double trunc(double x);
// Rounds toward zero.
float truncf(float x);
// Copies n bytes; the ranges must not overlap.
void *memcpy(void *d, const void *s, size_t n);
// Copies n bytes; the ranges may overlap.
void *memmove(void *d, const void *s, size_t n);
// Fills n bytes with c.
void *memset(void *d, int c, size_t n);

// ---- rt.c: allocation ----

// Exchanges two regions of n bytes that are the same region or do not overlap.
void kern_swap(char *a, char *b, int64_t n);
// Issues the next container identity tag: from 1 in a debug build, 0 (unchecked) in a release build or after wrap-around.
uint32_t kern_next_identity(void);
// Returns the system allocator, one object over the static arena.
void *kern_sysalloc_new(void);
// Allocates size bytes at align into the Result at out: Ok(pointer) or Err(OutOfMemory); traps explicit on a violated obligation.
void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at);
// Allocates zeroed bytes, as kern_sysalloc_alloc.
void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at);
// Resizes an allocation, keeping the common prefix, into the Result at out; on Err the old allocation stays.
void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at);
// Frees an allocation made with the same size and align; traps explicit for a block the arena does not hold.
void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at);

// ---- rt.c: clock ----

// Returns a monotonic clock in nanoseconds.
uint64_t kern_now_ns(void);
// Busy-waits until the board's clock has advanced by at least ns nanoseconds; returns at once on a board without a clock.
void kern_time_sleep_ns(uint64_t ns, const kern_loc *at);

// ---- f64text.c: floating-point text (linked only into an image that calls it) ----

// Writes the shortest decimal digits and exponent that round-trip v into buf: the length.
int64_t kern_str_f64_digits(double v, char *buf, int64_t n);
// Parses validated decimal text to the nearest double; never traps.
double kern_str_parse_f64(const char *p, int64_t n, const kern_loc *at);

#endif
