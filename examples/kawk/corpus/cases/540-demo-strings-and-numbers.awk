{
    printf "%-6s %-6s ", $1, $2
    if ($1 < $2) printf "less    "; else if ($1 == $2) printf "equal   "; else printf "greater "
    printf "%s %s\n", ($1 "" < $2 "") ? "less as strings," : "not less as strings,", ($1 + 0 == $2 + 0) ? "equal as numbers" : "unequal as numbers"
}
END {
    print (10 < 9), ("10" < "9"), (10 < "9"), ("abc" < 1), (2 < "abc")
    print (x == 0), (x == ""), length(x), x + 1, x "|"
    $0 = "0 00 0.0 abc"; print !$1, !$2, !$3, !$4, !"0", !0, !""
    n = split("10 9 1e1 0x1A", a, " "); print (a[1] > a[2]), (a[1] == a[3]), (a[4] == 26)
    y = "3.0"; z = 3; print (y == z), (y + 0 == z), ("3" == z), 1 " " 2 + 3, 1 2 + 3
}
