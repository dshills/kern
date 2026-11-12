function reverse(s,   r, i) { for (i = length(s); i > 0; i--) r = r substr(s, i, 1); return r }
function join(a, n, sep,   i, r) { for (i = 1; i <= n; i++) r = r (i > 1 ? sep : "") a[i]; return r }
function repeat(s, n) { return n <= 0 ? "" : s repeat(s, n - 1) }
{ if (NF) { print reverse($1); n = split($0, w); print join(w, n, "+") } } END { print repeat("ab", 5) }
