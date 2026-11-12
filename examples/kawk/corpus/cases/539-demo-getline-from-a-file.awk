BEGIN {
    for (i = 1; i <= 4; i++) print "row", i, i * i > tmp
    printf "%s %d %d\n", "row", 5, 25 >> tmp
    close(tmp)
    while ((getline line < tmp) > 0) { n++; split(line, f, " "); squares += f[3] }
    print n " lines read back, squares sum to " squares
    close(tmp); getline < tmp; print "getline < file sets $0: " $0 ", NF " NF ", NR " NR
    getline first < tmp; getline second < tmp; print "then: " first " / " second
    print "appended" >> tmp; close(tmp); n = 0; while ((getline line < tmp) > 0) n++; print n " lines now"
    print "a missing file gives " (getline line < "/no/such/file/kawk")
    close(tmp); printf "" > tmp; close(tmp)
}
NR == 1 { print "record 1: " $0; getline; print "after getline: " $0 ", NR " NR }
NR == 3 { getline held; print "getline var: " held ", $0 still " $0 ", NR " NR }
END { print "at the end getline gives " (getline) ", NR " NR }
