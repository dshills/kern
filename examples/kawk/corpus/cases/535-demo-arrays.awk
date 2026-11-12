function sorted(arr, keys,   n, i, j, t, k) {
    for (k in arr) keys[++n] = k
    for (i = 2; i <= n; i++) {
        t = keys[i]
        for (j = i - 1; j >= 1 && keys[j] > t; j--) keys[j + 1] = keys[j]
        keys[j + 1] = t
    }
    return n
}
{ for (i = 1; i <= NF; i++) count[tolower($i)]++ }
END {
    n = sorted(count, words)
    for (i = 1; i <= n; i++) if (count[words[i]] > 1) printf "%-6s %d\n", words[i], count[words[i]]
    print n " different words;", ("fox" in count) ? "a fox," : "no fox,", ("cat" in count) ? "a cat" : "no cat"
    delete count["the"]; print length(count) " after delete"
    for (r = 1; r <= 3; r++) for (c = 1; c <= 3; c++) grid[r, c] = r * c
    print grid[2, 3], ((3, 3) in grid), ((4, 1) in grid), length(grid)
    for (k in grid) { split(k, rc, SUBSEP); total += rc[1] * 10 + rc[2] }
    print total
    delete grid; print length(grid)
}
