Kind: implementation

Implement `parse` in `packet.kern`: the header is 8 bytes, big-endian: a 16-bit magic (must be 0xCAFE), a 16-bit length, a 32-bit sequence number. Return `None` when the buffer is shorter than 8 bytes or the magic is wrong. `main` is given and must print:

```
7
4660
bad
short
```
