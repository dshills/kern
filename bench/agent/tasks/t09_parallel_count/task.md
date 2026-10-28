Kind: implementation

Implement `parallel_count` in `par.kern`: split the vector's view into four parts of as equal a size as possible (when the length is not a multiple of four, the first parts take one extra element each), count the byte in each part on its own scoped thread (`thread.scope` and `Scope.spawn`, with `count_byte` as the thread function), and return the total; a spawn failure is returned as `Err`. The parts must be views of the vector, not copies: the compiler's copy analysis must report no copies beyond the three one-byte `SpawnErr` copies already in `main`, and its allocation analysis no allocation sites beyond the scope, the four spawns and the three sites in `fill` and `main` (eight in all), so no per-part vectors or boxes. The program must check cleanly (no `Send`/`Share` errors, no access held across the spawn) and print:

```
78
77
1
```
