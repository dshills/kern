Kind: debugging

`hist.kern` keeps a byte histogram in a 256-entry table and prints four lines after each of four updates, but it does not check: the compiler rejects it. Fix the program so that it checks cleanly and prints the output below, keeping `bump`, `count`, `reset`, `total`, `mode` and `share` as functions with their results (`count` must still add each byte through `bump`). The histogram is updated on every byte received, so the fix must also leave the compiler's bounds-check analysis reporting **at most 1 runtime-checked bounds check** in the whole program (the slice in `main` counts; keep `main` as it is). No `unsafe`. The program must check cleanly and print:

```
108
11
272
0
108
14
214
214
none
0
0
0
97
3
0
0
```
