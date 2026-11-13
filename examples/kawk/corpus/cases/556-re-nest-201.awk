# groups nested 201 deep, each kept by its second alternative: the
# deepest kawk takes (the system awk takes deeper ones too)
BEGIN {
    p = "a"
    for (i = 0; i < 201; i++) p = "(" p "|b)"
    print length(p), ("xax" ~ p), ("xbx" ~ p), ("xcx" ~ p)
    s = "cabac"
    n = gsub(p, "-", s)
    print n, s
    q = "a+"
    for (i = 0; i < 201; i++) q = "(" q ")"
    print match("xxaaay", q), RSTART, RLENGTH
    # the same depth around a count above 1000 with an operator after it
    # (2002 bytes: a text that awk's reading of a{1001}+ and kawk's, which
    # is (a{1001})+, match alike)
    r = "a{1001}+"
    for (i = 0; i < 201; i++) r = "(" r "|b)"
    t = sprintf("%2002s", "")
    gsub(/ /, "a", t)
    print ("xbx" ~ r), ("xcx" ~ r), match("c" t "c", r), RSTART, RLENGTH
}
