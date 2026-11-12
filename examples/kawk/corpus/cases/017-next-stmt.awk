NR % 2 { next } { print NR }
/dup/ { c++; next; c = 99 }
END { print c }
