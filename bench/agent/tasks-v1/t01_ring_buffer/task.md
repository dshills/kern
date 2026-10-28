Kind: implementation

Implement the ring buffer in `ring.kern`: `push` stores a value at the tail and returns false when the buffer is full, `pop` returns `Some(value)` from the head or `None` when empty, both wrapping around the 4-slot array. `main` is given and must print exactly:

```
true
true
true
true
false
1
2
true
3
4
5
empty
```
