BEGIN { inf = 1e300 * 1e300; nan = inf - inf; s = "abcdef"; print substr(s, nan, 2), substr(s, 2, nan) "|", substr(s, inf) "|", substr(s, -inf, 3), substr(s, 2, inf), substr(s, 2, -inf) "|" }
