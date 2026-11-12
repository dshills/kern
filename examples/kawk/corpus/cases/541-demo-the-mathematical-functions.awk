BEGIN {
    pi = atan2(0, -1)
    printf "pi %.6f  e %.6f  sqrt(2) %.6f  log(10) %.6f\n", pi, exp(1), sqrt(2), log(10)
    for (d = 0; d <= 180; d += 30) printf "%3d degrees: sin %9.6f cos %9.6f\n", d, sin(d * pi / 180), cos(d * pi / 180)
    printf "atan2 %.6f %.6f %.6f %.6f\n", atan2(1, 1), atan2(1, -1), atan2(-1, -1), atan2(-1, 1)
    printf "exp %.6g %.6g %.6g  log %.6g %.6g %.6g\n", exp(-1), exp(10), exp(0.5), log(2), log(0.001), log(1e10)
    printf "sqrt %.6g %.6g %.6g  int %d %d %d\n", sqrt(1e-6), sqrt(12345), sqrt(2e10), int(3.99), int(-3.99), int("42nd")
    print 2 ^ 10, 2 ^ 0.5, 2 ^ -2, 10 ^ 15, 2 ^ 3 ^ 2, -2 ^ 2, 7 % 3, -7 % 3, 7.5 % 2
    print sin(1e6), cos(1e6), exp(log(8) / 3), sqrt(1e300), log(exp(1)), 1e300 * 1e300, log(-1)
    print 1 / 3, 100 / 3, 1e6, 1e-6, 123456789, 0.1 + 0.2, 2 ^ 53 + 1, 1e30
}
