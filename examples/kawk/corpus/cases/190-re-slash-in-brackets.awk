BEGIN { s = "a/b"; print s ~ /a[\/]b/, s ~ "a[/]b", gsub("/", "-", s), s }
