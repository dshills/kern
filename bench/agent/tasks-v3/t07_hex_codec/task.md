Kind: refactoring

`hex.kern` encodes bytes as hex digits and decodes them back, with the buffer indexing written out inline in `encode` and `decode`. Refactor it: add `put_byte(dst, at, b) -> bool`, which writes the two digits of `b` at `dst[at]` and `dst[at + 1]` and reports whether there was room for both, and `get_byte(src, at) -> Option[u8]`, which reads the two digits at `src[at]` and `src[at + 1]` as one byte (`None` when there are not two bytes there, or either is not a hex digit), and make `encode` and `decode` do all their reading and writing of the buffers through them. The output must not change. The codec is on a hot path: the compiler's bounds-check analysis must report **no runtime-checked bounds checks** in the whole program (`main` has none; keep `main` as it is). No `unsafe`. The program must check cleanly and print:

```
10
54 98 54 53 55 50 54 101 50 49 
bad digit
2
107 101 
4
54 98 54 53 
3
74 75 76 
bad digit
0

```
