Kind: implementation

Implement the cache in `lru.kern`: `get` returns the value under a key and makes its entry the most recently used (counting a hit), or `None` and a miss; `put` updates an existing key (making it most recently used) or inserts a new entry at the head, evicting the least recently used entry first when the cache is full; `keys_in_order` appends the keys from most to least recently used. Entries live in the pool and are linked through their `prev`/`next` handles; an evicted entry is freed from the pool. Add helper methods as you see fit, but the compiler's allocation analysis must report **at most 4 allocation sites** in the whole program (the pool, its `alloc`, and the two in `show_keys`): no per-operation vectors or boxes. `main` is given and must print:

```
100
miss
300
333
400
4 3 1 
miss
500
5 6 4 
5
2
3
3
3
```
