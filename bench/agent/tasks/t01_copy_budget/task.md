Kind: refactoring

`sensor.kern` summarises a window of four 272-byte samples, before and after rescaling it, and it copies samples where it only needs to look at them. Change the program so that the compiler's copy analysis reports **at most 1 copy** in the whole program, without changing the output or removing any function (`sample_total`, `sample_peak`, `newest`, `oldest`, `window_total`, `window_peak`, `id_sum` must keep their names and results). Do not add `unsafe`. The program must check cleanly and print:

```
148160
363
5
84864
63
444480
363
11
254592
63
4
```
