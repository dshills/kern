{ if (NR % 2) print > "odd"; else print > "even" } END { close("odd"); close("even"); while ((getline a < "odd") > 0) o++; while ((getline b < "even") > 0) e++; print o, e }
