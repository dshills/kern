function find(arr, n, v,   i) { for (i = 1; i <= n; i++) if (arr[i] == v) return i; return -1 } BEGIN { n = split("a b c d", t); print find(t, n, "c"), find(t, n, "z") }
