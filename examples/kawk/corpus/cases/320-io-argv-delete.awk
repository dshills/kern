BEGIN { delete ARGV[1] } { n++ } END { print n }
