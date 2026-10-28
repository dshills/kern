Kind: optimization

`words.kern` prints word statistics for two texts, and it allocates on the heap for every word it visits. Rewrite it so that the compiler's allocation analysis reports **at most 1 allocation site** in the whole program (any call that may allocate counts as a site: constructing a container, pushing into one, cloning one, boxing a value), keeping the same output, the function `stats` and the `Stats` struct with its four fields, and no `unsafe`. The program must check cleanly and print:

```
9
5
11
102134282586790
11
9
20
82639991608527229
```
