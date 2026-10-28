Kind: debugging

`jobs.kern` does not check: the compiler reports several ownership errors. Fix the program so that it checks cleanly and prints exactly the output below. Every urgent, batched or drained job must be run exactly once and a scheduled job that is not urgent must not be run; every job must be destroyed exactly once; and the order of the lines is part of the requirement: a job that `run` consumes is destroyed when `run` returns, a job that is scheduled but not urgent is destroyed at the end of `schedule`, the batch's jobs are destroyed when `run_batch` returns, and each drained job is destroyed as soon as it has been run. Keep the signatures of `run`, `schedule`, `run_batch` and `drain`.

```
run 1
drop 1
scheduled 2
drop 2
run 3
run 4
drop 4
drop 3
run 6
drop 6
run 5
drop 5
190
```
