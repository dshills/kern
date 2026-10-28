Kind: refactoring

`stats.kern` copies a 4 KiB record on every call because `summarize` takes it by value. Change the program so the record is viewed rather than copied (the compiler's copy analysis must report at most 1 copy of `Record`), keeping the output:

```
4096
8
```
