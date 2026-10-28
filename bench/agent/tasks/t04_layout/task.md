Kind: optimization

`layout.kern` keeps list entries in a pool and checks a record header. Both `Entry` and `Header` are larger than they need to be because of padding between their fields. Reorder (or re-type, keeping every field's meaning) the fields so that, in the compiler's layout for the target, **`Entry` is at most 48 bytes and `Header` is at most 24 bytes**, without changing the output. Layout follows the language reference's Rule 4.1.5: fields in declaration order with C alignment and padding, a union's discriminant at offset 0 with its payload aligned to the largest payload alignment. The program must check cleanly and print:

```
4938341
1700049629
4
```
