BEGIN { print "a.b" ~ /a\.b/, "axb" ~ /a\.b/, "a+b" ~ /a\+b/, "a*b" ~ /a\*b/, "(x)" ~ /\(x\)/, "[x]" ~ /\[x\]/, "a\\b" ~ /a\\b/, "a/b" ~ /a\/b/, "a|b" ~ /a\|b/, "$" ~ /\$/, "^" ~ /\^/, "a?b" ~ /a\?b/, "{" ~ /\{/
  print "a\tb" ~ /a\tb/, "a\nb" ~ /a\nb/, "A" ~ /\101/, "a\"b" ~ /a\"b/, "a b" ~ /a\ b/, "q" ~ /\q/ }
