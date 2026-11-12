BEGIN { print "extra1\nextra2" > "extra"; close("extra"); ARGV[ARGC++] = "extra" } { print FILENAME == "extra" ? "E" : "I", $0 }
