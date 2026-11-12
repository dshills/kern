BEGIN { s = "a\nb"; print s ~ /a.b/, s ~ /^a$/, s ~ /a$/, s ~ /^b/, s ~ /a[^x]b/, match(s, /.+/), RLENGTH }
