{ a[NR] = $0 } END { for (i = 2; i <= NR; i++) { v = a[i]; for (j = i - 1; j > 0 && a[j] > v; j--) a[j + 1] = a[j]; a[j + 1] = v }; for (i = 1; i <= NR; i++) print a[i] }
