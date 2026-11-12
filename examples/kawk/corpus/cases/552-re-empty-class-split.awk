{ n = split($0, p, /[z-a]/); print n, p[1]; m = split($0, q, /x*/); print m, q[1], q[2] }
