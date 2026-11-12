{ for (i = 1; i <= NF; i++) { len += length($i); if ($i ~ /^[0-9]+$/) nums += $i } } END { print NR, len, nums }
