Kind: review

`buf.kern` is a block of bytes over a raw allocation whose raw-pointer operations are wrapped in the `block_` functions, plus byte helpers over views of a message. It does not check: some wrappers perform unsafe operations outside an `unsafe` block, and wrappers that contain `unsafe` lack the contract block that states what the raw operations rely on. Review the module and make it check cleanly: put each raw operation inside an `unsafe` block (no new raw-pointer operations, no change to what the functions compute) and give every function that contains `unsafe` a contract that states its obligations. The block functions `block_new`, `block_put`, `block_get`, `block_free` and `copy_in` and the byte helpers `count_bytes`, `translate`, `ends_with`, `strip_len`, `xor_key` and `wrap_get` must keep their signatures and results. The helpers run on every message received, so the compiler's bounds-check analysis must also report **no runtime-checked bounds checks** in the whole program (keep `main`, `show`, `bit` and `pair` as they are). The program must then check cleanly and print:

```
8 111
1 0
33 0
0 0
2 0
3 0
3 4
65 45 66 
1 5
42 39 51 44 64 104 
51 44
0 5
75 69 82 78 33 
0 0
```
