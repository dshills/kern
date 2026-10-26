Kind: review

`raw.kern` uses raw pointers and does not check: the unsafe wrapper lacks its contract block and one operation sits outside `unsafe`. Make the program check cleanly: add contract blocks that state the obligations of each unsafe wrapper and put the pointer loads inside an `unsafe` block (no new raw-pointer operations), so it prints:

```
15
```
