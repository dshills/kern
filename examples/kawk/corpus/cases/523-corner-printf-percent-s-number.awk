BEGIN { x = 0.1 + 0.2; printf "%s %s %s %s\n", x, 1e20, 1e-20, 100; CONVFMT = "%.2g"; printf "%s %s %d\n", x, 3.14159, 3.99; printf "%s\n", 12 }
