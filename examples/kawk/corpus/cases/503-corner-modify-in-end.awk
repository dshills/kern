END { $2 = "Z"; print; print NF; $0 = "new record here"; print $2, NF }
