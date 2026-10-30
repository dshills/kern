Kind: implementation

Implement `sort`, `dedup`, `contains`, `rank` and `pair_sum` in `set.kern`, keeping their signatures: `sort(xs: edit[]u64)` puts `xs` in ascending order with an insertion sort (each element in turn moves left past the larger elements before it); `dedup(xs: edit[]u64) -> usize` removes the repeated keys of a sorted sequence in place, moving the distinct keys to the front in order and returning their number (what is left after them does not matter); `contains(xs: view[]u64, key: u64) -> bool` reports whether `key` is in a sorted sequence and `rank(xs: view[]u64, key: u64) -> usize` is the number of its elements less than `key`, both found by binary search; `pair_sum(xs: view[]u64, target: u64) -> bool` reports whether two elements at different positions of a sorted sequence add up to `target`, found by walking one position up from the smallest element and one down from the largest. The set code is on a hot path: the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program; keep `main`, `show` and `query` as they are. No `unsafe`. The program must check cleanly and print:

```
3 3 7 7 11 19 25 42 42 42 
true 2 false
false 6 false
false 7 true
true 7 false
false 10 false
6
3 7 11 19 25 42 
10 30 50 70 90 
true 0 false
false 1 false
false 3 true
false 5 true
5
10 30 50 70 90 
false 0 false
0
```
