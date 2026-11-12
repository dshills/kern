BEGIN { print ARGC; for (i = 1; i < ARGC; i++) print i, ARGV[i]; print length(ARGV) }
