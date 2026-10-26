# Workload: thread_pool

## Summary
A fixed set of worker threads pulling jobs from a shared queue. A job is a
plain function pointer plus an owned context (`Box[Task]`): no closures, no
captured references, per §4 and ADR-10. The queue is `Shared[Mutex[Vec[Job]]]`;
shutdown is a `Stop` job per worker; results come back through a second
`Mutex` or through `Join[R]` for scoped batches. This is the §5.8 case
"concurrent container access" on the mutex path.

## Classification
expressible in the core

## Data structures and ownership graph
```text
union Job { Run(f: func(own Box[Task]), ctx: Box[Task]); Stop }    // Send iff Task: Send
struct Task { input: Vec[u8]; reply: Shared[Mutex[Vec[u64]]] }    // owned by the job, then by f
struct Pool { queue: Shared[Mutex[Vec[Job]]]; workers: Vec[Thread[()]] }
```
- `Pool` owns the worker `Thread[()]` handles and one `Shared` count on the
  queue; each worker owns another `Shared` clone (moved in at spawn). The
  last owner destroys the mutex and any undrained jobs.
- A `Job` owns its `Box[Task]`; `submit` moves it into the vector under the
  lock; a worker moves it out under the lock and then runs `f(ctx)` with the
  lock released. Nothing is shared between the submitter and the worker
  except through `Shared[Mutex[...]]`.
- `Job` is a union, not a struct, so that `match move job { Run(f, ctx) => f(ctx) }`
  moves both payload fields out at once; a struct would need `take` on a
  field, and `Box[Task]` has no default (`OWN_PARTIAL_MOVE` otherwise).

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `submit(view self, f, own ctx: Box[Task]) -> Result[(), AllocErr]` | `view` + `with g := self.queue.get().lock()` | `g: edit[Vec[Job]]` for the block; `?` inside releases the lock |
| `worker(own q: Shared[Mutex[Vec[Job]]])` | `own` | the spawn function; `q` must be `Send` |
| `thread.spawn(worker, q.clone())` | `own` args | `SEND_NOT_SEND` if `Task` held a `RawPtr` |
| `shutdown(own self)` | `own` | pushes one `Stop` per worker, then joins each `Thread[()]` |
| `Mutex.lock(view self) -> edit[T] release unlock` | release accessor (std) | the only escalation, inside `std` |
No `get2`. The job's `f` receives `own Box[Task]`, so no access crosses a
thread (`ACC_LIVE_ACROSS_SPAWN` cannot occur: nothing is borrowed).

## Why this classification
The spec designs this case explicitly: "Callback and thread-pool patterns use
a pair of a function pointer and an owned context value (§5.5)". `Shared[T]`
is `Send` iff `T: Send + Share`; `Mutex[T]` is `Send` and `Share` iff `T: Send`;
`Vec[Job]` is `Send` iff `Job` is; `Job` is `Send` because function pointers
are and `Box[Task]` is when `Task` is (§5.5 derivation, types.md §3). The
checker derives all of it structurally with no annotations in the workload.
`Mutex.lock` is the runtime-exclusive escalation of §5.3, implemented once in
`std/mutex.kern`; the workload's source contains no `unsafe`. The §5.6
extension is irrelevant: no job ever holds a reference.

## Sketch
```text
module pool_bench

func worker(own q: Shared[Mutex[Vec[Job]]]) {
    while true {
        var job: Option[Job]
        with g := q.get().lock() {                      // edit[Vec[Job]] rooted at the mutex
            job = g.pop()                               // Option[Job] moved out of the vector
        }                                               // unlock runs here, before the job runs
        match move job {
            Some(j) => {
                match move j {                          // Job has no destructor: destructuring allowed
                    Run(f, ctx) => f(ctx)               // ctx moved into the callee
                    Stop        => return
                }
            }
            None => thread.yield()
        }
    }
}

impl Pool {
    func submit(view self, f: func(own Box[Task]), own ctx: Box[Task]) -> Result[(), AllocErr] {
        with g := self.queue.get().lock() {
            g.push(Job.Run(f, ctx))?                    // early exit on Err still runs unlock
        }
        return Result.Ok(())
    }

    func shutdown(own self) {
        for i in 0..self.workers.len() {
            with g := self.queue.get().lock() { g.push(Job.Stop) }
        }
        while self.workers.len() > 0 {
            match self.workers.pop() { Some(t) => t.join()  None => {} }   // Thread[()].join(own self)
        }
    }
}

```
`start(n)` is `for i in 0..n { workers.push(thread.spawn(worker, q.clone())?) }`
over a `q := Shared.new(Mutex.new(Vec[Job].new(SystemAllocator{})))`. `job` is
declared with `var` and assigned inside the `with` block so the lock is
released before the job runs; matching `g.pop()` directly would hold it.

## Benchmark notes
- C: pthreads, a mutex plus condition variable, and `struct job { void (*f)(void*); void* ctx; }`.
- Rust: a hand-written `Arc<Mutex<Vec<Job>>>` pool with `Box<dyn FnOnce>`
  jobs (the idiomatic form) and a `fn(Box<Task>)` variant matching kern exactly.
- Measure: jobs per second for 1 µs and 100 µs jobs with 1, 4, 8 workers,
  p50/p99 submit-to-start latency, peak RSS, allocation count (one `Box` per
  job plus queue growth; same as C's `malloc` per context), copies (zero: the
  `Box` is moved), unsafe lines (zero outside `std`), text bytes, compile time.
- Failure paths: `SpawnErr` on the k-th spawn must join the k-1 already
  started (the `?` in `start` destroys `workers`, whose `Thread` destructor
  detaches; the fixture asserts the detached workers exit on `Stop`); a job
  that traps terminates the process (§6), reported as semantics.

## Open risks
- There is no condition variable in §5.5; the idle path spins on `thread.yield`.
  Phase 3 should add `Condvar` (or `Mutex.wait`) to `std` or the idle-latency
  row will be unfavorable; it does not change the classification.
- `Thread[()]` destruction detaches (§5.5), so dropping a `Pool` without
  `shutdown` leaks running workers; `destructor(edit self)` should push `Stop`
  jobs. LIFO `Vec.pop` is not FIFO; a fair pool uses `Ring[Job]` under the mutex.
