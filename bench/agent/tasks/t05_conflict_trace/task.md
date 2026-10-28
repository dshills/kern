Kind: debugging

`inventory.kern` does not check: the compiler rejects it. Fix the program so it checks cleanly and prints the output below, keeping its behaviour: `fulfil` must still restock an item (by 5) when an order empties it, `total_qty` must still be computed from the orders, and the orders vector must still be alive to be counted at the end. Do not clone the orders or the items (the hidden tests bound the program's allocation sites and copies to what it has now).

```
21
130
321
2
4
```
