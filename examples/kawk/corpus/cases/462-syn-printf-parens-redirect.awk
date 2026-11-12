BEGIN { printf("%s %s\n", "a", "b") > "/dev/stdout"; printf("%d\n", 1 > 0); printf "%d %d\n", (2 > 1), (1 > 2); print(1 > 2 ? "x" : "y") }
