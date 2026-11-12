BEGIN { print "a{" ~ /a{/, "a{x}" ~ /a{x}/, "{" ~ /{/, "a{,2}" ~ /a{,2}/, "aa" ~ /^a{,2}$/, "}" ~ /}/, "a{2}" ~ /a\{2\}/, match("xaaay", /a{2}/), RLENGTH }
