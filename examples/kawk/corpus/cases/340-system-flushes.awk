BEGIN { printf "no newline yet "; system("echo shell"); print "end" } END { print "in end"; system("echo last") }
