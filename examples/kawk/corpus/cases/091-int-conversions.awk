BEGIN { print int(3.9), int(-3.9), int("3abc"), int(""), int(1e30), int(-1e30), int(0.999999), int(2 ^ 53 + 1), int("  42  "), int(1e18 + 0.5), int(-0.5) }
