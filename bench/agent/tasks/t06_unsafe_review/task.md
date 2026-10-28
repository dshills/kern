Kind: review

`stack.kern` is a stack over a raw allocation whose raw-pointer operations are wrapped in the `Stack` methods. It does not check: some wrappers perform unsafe operations outside an `unsafe` block, and wrappers that contain `unsafe` lack the contract block that states what the raw operations rely on. Review every wrapper and make the program check cleanly: put each raw operation inside an `unsafe` block (no new raw-pointer operations, no change to what the methods compute) and give every function that contains `unsafe` a contract that states its obligations. The program must then print:

```
true
true
true
true
false
13
36
13
11
12
none
```
