BEGIN { FS = "|" } { print NF, $2 } END { FS = "."; $0 = "a.b.c"; print NF, $3; FS = "*"; $0 = "a*b"; print NF; FS = "\\"; $0 = "a\\b\\c"; print NF }
