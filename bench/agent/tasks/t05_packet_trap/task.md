Kind: debugging

`pkt.kern` validates packets (a 16-bit big-endian payload length, the payload, then one checksum byte) and prints three lines for each of six buffers, but it traps with kind `bounds` on the third buffer, a packet cut off before its checksum. Fix the program so that it prints the output below for all six buffers, keeping `rd16`, `checksum`, `valid`, `payload_sum` and `first_byte` with their signatures. The validator runs on every packet received, so the fix must also leave the compiler's bounds-check analysis reporting **at most 6 runtime-checked bounds checks** in the whole program (the six slices in `main` count; keep `main` as it is). No `unsafe`. The program must check cleanly and print:

```
true
60
10
false
0
none
false
0
none
true
0
none
false
0
none
true
60
10
```
