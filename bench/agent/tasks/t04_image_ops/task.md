Kind: optimization

`image.kern` blurs, scrolls and flips images stored as flat grids of `w * h` pixels, row by row, sums their rows, and prints four lines for each of two images. Nearly every pixel access is bounds-checked at run time. Restructure the program so that the compiler's bounds-check analysis reports **no runtime-checked bounds checks** in the whole program (keep `main`, `show` and `report` as they are), without changing the output and without `unsafe`. Keep `blur(src, dst, w, h)`, `flip(g, w, h)`, `row_sums(g, w, h, sums) -> usize` and `scroll(src, dst, k)` with their signatures and results: `blur` writes the 3x3 box blur with wrap-around edges, `flip` mirrors the grid left to right in place, `row_sums` writes the sum of each row that has an entry in `sums` and returns how many it wrote, and `scroll` writes `src` moved back by `k` pixels with wrap-around. The program must check cleanly and print:

```
84 78 87 96 90 69 63 72 81 75 114 108 117 126 120 99 93 102 111 105 
4: 435 360 585 510 
63 72 81 90 99 108 117 126 135 144 153 162 171 180 9 18 27 36 45 54 
45 36 27 18 9 90 81 72 63 54 135 126 117 108 99 180 171 162 153 144 
11 11 31 33 33 14 8 7 57 58 59 9 
1: 133 
2 6 5 11 4 7 0 3 250 1 8 40 
8 1 250 3 0 7 4 11 5 6 2 40 
```
