{ for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+$/) b[int($i / 10)]++ } END { for (i = 0; i < 10; i++) { printf "%2d-%2d: ", i * 10, i * 10 + 9; for (j = 0; j < b[i]; j++) printf "*"; print "" } }
