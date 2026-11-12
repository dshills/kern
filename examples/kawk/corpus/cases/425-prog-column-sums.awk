{ for (i = 1; i <= NF; i++) if ($i == $i + 0) { sum[i] += $i; cnt[i]++ }; if (NF > m) m = NF } END { for (i = 1; i <= m; i++) printf "col %d: n=%d sum=%g\n", i, cnt[i], sum[i] }
