BEGIN { FS = ":"; n = split("a:b c:d", p); print n, p[2]; FS = "[0-9]"; n = split("a1b2c", p); print n, p[3]; FS = " "; n = split(" x  y ", p); print n }
