{
    if (NF > 4)
        print "a"
    else
        print "b"
    if (NR % 2) { x++ }
    else { y++ }
}
END { print x, y }
