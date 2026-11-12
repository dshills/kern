function sort_keys(arr, keys,   n, i, j, t, k) {
    n = 0
    for (k in arr) keys[++n] = k
    for (i = 2; i <= n; i++) {
        t = keys[i]
        for (j = i - 1; j >= 1 && keys[j] "" > t ""; j--) keys[j + 1] = keys[j]
        keys[j + 1] = t
    }
    return n
}
BEGIN { split("pear apple fig banana cherry", w, " "); for (i in w) inv[w[i]] = i; n = sort_keys(inv, k); for (i = 1; i <= n; i++) print k[i], inv[k[i]] }
