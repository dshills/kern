BEGIN { re = "^(alpha|beta|gamma|delta|epsilon|zeta|eta|theta|iota|kappa|lambda|mu)$"; n = split("alpha beta eta etas mu nu kappa lambd", w, " "); for (i = 1; i <= n; i++) printf "%d", w[i] ~ re; print ""
  print match("say theta now", /theta|the|eta/), RLENGTH }
