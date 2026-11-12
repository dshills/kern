BEGIN { print "data" > "f"; close("f"); system("cat f; wc -l < f | tr -d ' '"); system("rm f"); print (getline x < "f") }
