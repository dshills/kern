Kind: debugging

`heap.kern` keeps binary min-heaps in sequences (the children of slot i are slots 2i + 1 and 2i + 2) and prints the results of building, pushing to and popping from five sequences, but its heaps come out wrong: `pop` returns the values out of order (the first heap pops `5 7 3 8` instead of `1 2 3 5`), and `is_heap` reports `false` right after `heapify`. Fix the program so that it prints the output below, keeping `sift_down`, `heapify`, `push`, `pop` and `is_heap` with their signatures and results. The queue runs on every scheduling decision, so the fix must also leave the compiler's bounds-check analysis reporting **no runtime-checked bounds checks** in the whole program (keep `main` and `show` as they are). No `unsafe`. The program must check cleanly and print:

```
false
true
1 2 3 5
7 8 9 9 
true
5 10 25 40 20 30 
5 10 20
4 5 6 7 9 8 
1 3 2 9 4 5 
true
10 20 30 50 40 
```
