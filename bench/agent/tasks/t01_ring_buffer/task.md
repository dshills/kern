Kind: implementation

Implement the four `TODO` methods of `Ring` in `ring.kern`: `push` appends a value, overwriting the oldest element and advancing `head` when the ring is full; `get` returns the i-th oldest element, or `None` when `i` is not below `count`; `sum` adds up the elements held; `min` returns the smallest element held, or `None` when the ring is empty. Index `buf` through the given `slot` method, keeping its signature. The ring is on a hot path: the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program (`main` has none; keep `main` as it is). No `unsafe`. The program must check cleanly and print:

```
40
110
none
600
40
3
19
3
none
none
0
```
