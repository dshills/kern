BEGIN { for (x = -5; x <= 5; x += 0.75) printf "%g %g | ", exp(x), log(x + 5.5); print ""; print exp(0), exp(1), exp(-1), exp(10), exp(-10), exp(700), exp(-700), exp(1000), exp(-1000), log(1), log(10), log(0.5), log(1e100), log(1e-100), log(0), log(-1)
  printf "%.12g %.12g %.12g %.12g\n", exp(1), log(2), exp(log(7)), log(exp(3)); print exp("1"), log("x") }
