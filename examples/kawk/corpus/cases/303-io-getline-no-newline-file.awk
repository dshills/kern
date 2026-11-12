BEGIN { printf "one\ntwo" > "f"; close("f"); while ((getline x < "f") > 0) n++; print n, x; printf "" > "g"; close("g"); print (getline y < "g") }
