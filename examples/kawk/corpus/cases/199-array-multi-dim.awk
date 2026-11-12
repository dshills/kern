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
BEGIN { for (i = 1; i <= 3; i++) for (j = 1; j <= 3; j++) m[i, j] = i * j; print m[2, 3], m[3, 3], length(m); print ((2, 2) in m), ((4, 1) in m), (2 SUBSEP 2) in m
  n = sort_keys(m, keys); for (i = 1; i <= n; i++) { split(keys[i], ij, SUBSEP); printf "%s,%s=%s ", ij[1], ij[2], m[keys[i]] }; print ""; delete m[2, 2]; print length(m), ((2, 2) in m); k = 1; print m[k, k + 1], m[k "", "2"] }
