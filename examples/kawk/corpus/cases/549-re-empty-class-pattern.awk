/[z-a]/ { printf "%s ", $0 } /[c-a-z]/ { printf "<%s> ", $0 } /^[^z-a]$/ { printf "(%s) ", $0 } END { print "" }
