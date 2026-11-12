BEGIN { x = -0; print x; y = 0; z = -y; print z; printf "%g %d %s %f\n", z, z, z, z; print 0 * -1, -0.0, 1 / (0 * -1) < 0 }
