BEGIN { n = 200; for (i = 2; i <= n; i++) if (!c[i]) { printf "%d ", i; for (j = i * i; j <= n; j += i) c[j] = 1 }; print "" }
