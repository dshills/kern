{ for (i = 1; i <= NF; i++) printf "%s:%d:%d ", $i, ($i == $i + 0), ($i < 10); print "" }
