BEGIN { print "b\na" | "sort"; print "written" > "f"; print "stdout line" } END { system("cat f") }
