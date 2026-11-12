/[[:digit:]]/ { a++ } /[[:upper:]]/ { b++ } /^[[:alpha:][:space:]]+$/ { c++ } /[[:punct:]]/ { d++ } /^[[:alnum:]]+$/ { e++ } /[[:blank:]]$/ { f++ } /[^[:print:]]/ { g++ } /[[:xdigit:]]{4}/ { h++ } /[[:lower:]][[:digit:]]/ { i++ } /[[:cntrl:]]/ { j++ } /[[:graph:]]/ { k++ }
END { print a, b, c, d, e, f, g, h, i, j, k }
