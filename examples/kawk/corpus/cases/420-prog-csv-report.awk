BEGIN { FS = "," } NR > 1 { n[$3]++; total[$3] += $4; if ($4 > max[$3]) { max[$3] = $4; who[$3] = $2 } }
END { for (d in n) printf "%-6s n=%d avg=%8.2f max=%8.2f (%s)\n", d, n[d], total[d] / n[d], max[d], who[d] | "sort"; close("sort"); printf "%-6s %d\n", "all", NR - 1 }
