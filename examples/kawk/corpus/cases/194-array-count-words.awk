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
{ for (i = 1; i <= NF; i++) count[$i]++ } END { n = sort_keys(count, keys); for (i = 1; i <= n; i++) if (count[keys[i]] > 1) print keys[i], count[keys[i]] }
