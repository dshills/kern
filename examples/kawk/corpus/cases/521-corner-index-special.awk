BEGIN { print index("a.b", "."), index("a*b", "*"), index("a\\b", "\\"), index("a[b", "["), index("tab\there", "\t"), index("aXbXc", "Xc"), index("abc", "c"), index("abcabc", "ca") }
