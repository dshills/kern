{ a[NR] = $1 } END { n = NR; for (i = 1; i <= n; i++) for (j = n; j > i; j--) if (a[j] < a[j - 1]) { t = a[j]; a[j] = a[j - 1]; a[j - 1] = t }; for (i = 1; i <= n; i++) printf "%s ", a[i]; print "" }
