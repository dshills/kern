BEGIN { printf "a;b;c" > "f"; close("f"); RS = ";"; while ((getline x < "f") > 0) printf "[%s]", x; print ""; RS = "\n" }
