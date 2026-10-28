Kind: review

`bytes.kern` is a byte buffer over a raw allocation whose raw-pointer operations are wrapped in the `Buf` methods, plus two search helpers over views of a text. It does not check: some wrappers perform unsafe operations outside an `unsafe` block, and wrappers that contain `unsafe` lack the contract block that states what the raw operations rely on. Review the module and make it check cleanly: put each raw operation inside an `unsafe` block (no new raw-pointer operations, no change to what the methods compute) and give every function that contains `unsafe` a contract that states its obligations. The safe helpers `find(src, pat)` and `count_byte(src, b)` are under review too: the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program (`main` has none; keep `main` as it is), and the helpers must keep their signatures and results. The program must then check cleanly and print:

```
8
104
111
none
794
none
0
4
6
none
0
none
2
3
0
```
