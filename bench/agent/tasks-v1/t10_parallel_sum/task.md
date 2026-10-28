Kind: implementation

Implement `parallel_total` in `par.kern`: split the vector's view into two halves, sum each half on its own scoped thread (`thread.scope` and `Scope.spawn`), and return the sum of both results; on a spawn failure return `Err`. The program must check cleanly (no Send/Share errors, no accesses held across the spawn) and print:

```
499500
```
