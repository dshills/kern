BEGIN { print "before"; r = system("echo from system"); print "after", r; r = system("exit 3"); print r; r = system("true"); print r; r = system("false"); print r; print system("echo a; echo b") }
