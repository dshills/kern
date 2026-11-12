BEGIN { i = 1; print i + i++, i; i = 1; print i++ + i, i; j = 2; print j * j++, j; k = 5; x = k - k--; print x, k; u = 1; v = u u++; print v, u; w = 3; print w++ w, w-- - w }
