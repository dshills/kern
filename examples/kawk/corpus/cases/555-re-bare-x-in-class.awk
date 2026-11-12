/[\xg]/ { printf "C[%s] ", $0 } /^[ab\xg]$/ { printf "<%s> ", $0 } /^[ab\xcd]$/ { printf "{%s} ", $0 } END { print "" }
