// kern hosted runtime (PLAN.md 2.2, specs/runtime): traps, the standard
// streams, the runtime-provided library objects (Shared, Mutex, Condvar),
// threads, the system allocator, the operating-system interface of the std
// modules and the test hooks. Linked by `kern build` with every program.
// The containers (Box, Vec, Pool, Arena) are kern code in std/core.kern.
//
// This file is the one translation unit clang compiles: the interface,
// then each unit in an order that puts every static helper before its
// uses.
#include "kern_rt.h"
#include "rt_base.c"
#include "rt_io.c"
#include "rt_proc.c"
#include "rt_trap.c"
#include "rt_alloc.c"
#include "rt_time.c"
#include "rt_str.c"
#include "rt_sync.c"
#include "rt_thread.c"
#include "rt_start.c"
