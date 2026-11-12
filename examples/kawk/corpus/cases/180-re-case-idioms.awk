{ if (tolower($0) ~ /the/) n++ } /[Tt][Hh][Ee]/ { m++ } END { print n, m }
