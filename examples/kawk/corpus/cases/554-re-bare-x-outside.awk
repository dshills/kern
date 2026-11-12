/\xg/ { printf "[%s] ", $0 } /a\xb/ { printf "A[%s] ", $0 } /^\x$/ { printf "B[%s] ", $0 } /\x41|\x6d/ { printf "(%s) ", $0 } END { print "" }
