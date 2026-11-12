BEGIN { FS = "[0-9]+" } { print NF ":" $1 "|" $NF }
