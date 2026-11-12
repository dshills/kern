/^name:/ { split($0, f, ":"); rec = f[2]; next } /^The/ { inside = 1 } inside && /^[0-9]/ { sum[rec] += $1 } /^dup/ { inside = 0 } END { print rec, sum[rec] + 0 }
