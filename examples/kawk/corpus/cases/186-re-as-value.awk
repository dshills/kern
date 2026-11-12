{ x = /a/; y = (/a/) + (/e/) + (/o/); if (x) cnt[y]++ } END { for (k = 0; k <= 3; k++) print k, cnt[k] + 0 }
