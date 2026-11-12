BEGIN { FS = ":"; $0 = "a:b:c"; print NF, $2; FS = "-"; print $2; $0 = "x-y"; print $2 }
