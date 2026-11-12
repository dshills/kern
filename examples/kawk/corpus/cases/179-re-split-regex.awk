BEGIN { n = split("a1b22c333d", p, /[0-9]+/); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n
  n = split("a, b ,c,d", p, / *, */); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n
  n = split(":a::b:", p, /:/); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n
  n = split("abc", p, /x*/); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n
  n = split("aXbxc", p, "[xX]"); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n
  n = split("a.b.c", p, "."); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n
  n = split("a b  c", p, / /); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n
  n = split("^a^b", p, /^\^|b/); for (i = 1; i <= n; i++) printf "[%s]", p[i]; print n }
