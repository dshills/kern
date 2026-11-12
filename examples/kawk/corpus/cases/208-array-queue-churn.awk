BEGIN { head = tail = 0; for (i = 0; i < 5000; i++) { q[tail++] = i; if (i % 3 == 0) { s += q[head]; delete q[head++] } }; print length(q), s, head, tail; n = 0; for (k in q) n++; print n }
