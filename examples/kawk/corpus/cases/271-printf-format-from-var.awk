BEGIN { fmt = "%s has %d items (%.1f%%)\n"; printf fmt, "list", 3, 42.857; f2 = "%" 8 "." 3 "f|\n"; printf f2, 3.14159; printf("%s\n", "paren form"); printf "%s " "%s\n", "concat", "format" }
