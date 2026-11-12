NR == 1 { FS = ":"; print $1 } NR == 2 { print $1 } NR == 7 { print $2; FS = " "; $0 = $0; print $2 }
