Kind: debugging

`scores.kern` ranks score tables by repeatedly moving the best remaining score to the front, all through `argmax_from`, and prints the results for two tables, but its rankings come out wrong: `sort_desc` leaves the first table as `3 98 75 75 61 40 20 12` instead of sorting it from the largest value down, `top_k` repeats values (`30 30 18 7` for the second table), and `argmax_from(xs, 4)` on the first table names index 6 although the largest value at index 4 or later is at index 4. Fix the program so that it prints the output below, keeping `argmax_from`, `sort_desc`, `top_k`, `max_score` and `lead` with their signatures and results (`sort_desc` and `top_k` must still find each value through `argmax_from`). The ranking runs on every leaderboard refresh, so the fix must also leave the compiler's bounds-check analysis reporting **no runtime-checked bounds checks** in the whole program (keep `main` and `show` as they are). No `unsafe`. The program must check cleanly and print:

```
3 4
98 23
4 30 12
98 75 75 
30 18 7 7 
98 75 75 61 40 20 12 3 
30 18 7 7 1 
```
