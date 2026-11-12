BEGIN { printf "a"; fflush(); system("printf b"); printf "c"; print fflush(), fflush("/dev/stdout"); print "d" > "f"; fflush("f"); system("cat f"); print "e" }
