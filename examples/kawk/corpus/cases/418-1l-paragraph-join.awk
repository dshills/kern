BEGIN { RS = ""; FS = "\n"; OFS = " / " } { $1 = $1; print }
