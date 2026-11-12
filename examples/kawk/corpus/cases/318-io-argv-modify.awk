BEGIN { ARGV[1] = ""; ARGV[2] = "v=7"; ARGC = 4 } { n++ } END { print n, v }
