BEGIN { FS = "" } NR < 3 { print NF, $1, $2, $NF }
