Kind: refactoring

`conflict.kern` is rejected with an access conflict. Restructure it so it checks cleanly without changing what it prints:

```
30
30
```

Do not copy the vector; the output must come from the same `Vec`.
