BEGIN { printf "x,y,z" > "f"; close("f"); RS = ","; while ((getline v < "f") > 0) s = s "[" v "]"; print s; RS = "\n" } { n++ } END { print n }
