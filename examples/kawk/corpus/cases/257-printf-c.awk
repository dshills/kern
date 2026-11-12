BEGIN { printf "%c%c%c|%c|%c|%5c|%-5c|%c\n", 72, 105, 33, "hello", "", "x", "y", 65.9; printf "%c%c", 256 + 65, 0 + 66; printf "\n"; x = "A"; printf "%c %c\n", x, x + 0 }
{ if (NR == 2) printf "%c%c%c\n", $1, $2, 48 + $3 }
