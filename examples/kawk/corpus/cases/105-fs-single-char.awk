BEGIN { FS = ":" } { print NF, $2, $NF }
